package carstore

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	cid "github.com/ipfs/go-cid"
	datastore "github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/query"
	logging "github.com/ipfs/go-log/v2"
	"github.com/ipld/go-car/v2/storage"
	ipld "github.com/ipld/go-ipld-prime"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/ipld/go-ipld-prime/multicodec"
	"github.com/ipld/go-ipld-prime/node/basicnode"
	"github.com/ipni/go-libipni/ingest/schema"
	"github.com/ipni/storetheindex/filestore"
	"github.com/libp2p/go-libp2p/core/peer"
)

var log = logging.Logger("carwriter")

type WriteError struct {
	err error
}

func (e WriteError) Error() string {
	return e.err.Error()
}

func (e WriteError) Unwrap() error {
	return e.err
}

// CarWriter writes advertisements and entries, that are stored in a datastore,
// to CAR files. Each advertisement and its associated multihash entries
// blocks, or HAMT, are written to a single CAR file. The original chunks or
// HAMT are preserved, as opposed to storing only multihashes, for the purpose
// of being able to verify links from advertisements and verify the
// advertisement signature. Such verification may be necessary when fetching
// car files from a location that is not completely trusted.
type CarWriter struct {
	compAlg   string
	dstore    datastore.Datastore
	fileStore filestore.Interface
}

// NewWriter create a new CarWriter that reads advertisement data from the
// given datastore and writes car files to the specified directory.
func NewWriter(dstore datastore.Datastore, fileStore filestore.Interface, options ...Option) (*CarWriter, error) {
	opts, err := getOpts(options)
	if err != nil {
		return nil, err
	}
	return &CarWriter{
		compAlg:   opts.compAlg,
		dstore:    dstore,
		fileStore: fileStore,
	}, nil
}

func (cw *CarWriter) Compression() string {
	return cw.compAlg
}

// Write reads the advertisement, specified by CID, from the datastore and
// writes it and its entries into a CAR file. The car file is stored in the
// directory specified when the CarWriter was created, and is named with the
// advertisement CID.
//
// If the car file already exists, it is not overwritten and fs.ErrExist is
// returned. When this happens the filestore.File information describing the
// existing file is also returned.
//
// The CAR file is written without entries if skipEntries is true. The purpose
// of this to create a car file, to maintain the link in the advertisement
// chain, when it is know that a later advertisement deletes this
// advertisement's entries.
func (cw *CarWriter) Write(ctx context.Context, adCid cid.Cid, skipEntries bool) (*filestore.File, error) {
	ad, data, err := cw.loadAd(ctx, adCid)
	if err != nil {
		return nil, fmt.Errorf("cannot load advertisement: %w", err)
	}
	return cw.write(ctx, adCid, ad, data, skipEntries)
}

func (cw *CarWriter) write(ctx context.Context, adCid cid.Cid, ad schema.Advertisement, data []byte, skipEntries bool) (*filestore.File, error) {
	fileName := adCid.String() + CarFileSuffix
	carPath := fileName
	if cw.compAlg == Gzip {
		carPath += GzipFileSuffix
	}
	roots := make([]cid.Cid, 1, 2)
	roots[0] = adCid

	var entriesCid cid.Cid
	if !skipEntries && ad.Entries != nil && ad.Entries != schema.NoEntries {
		entriesCid = ad.Entries.(cidlink.Link).Cid
		roots = append(roots, entriesCid)
	}

	// If the destination file already exists, do not rewrite it.
	fileInfo, err := cw.fileStore.Head(ctx, carPath)
	if err != nil {
		if err != fs.ErrNotExist {
			return nil, err
		}
		// OK, car file does not exist.
	} else {
		if err = cw.removeAdData(roots); err != nil {
			log.Errorw("Cannot remove advertisement data from datastore", "err", err)
		}
		return fileInfo, fs.ErrExist
	}

	carTmpName := filepath.Join(os.TempDir(), fileName)
	carFile, err := os.Create(carTmpName)
	if err != nil {
		return nil, fmt.Errorf("cannot create car file: %w", err)
	}
	defer os.Remove(carTmpName)
	defer carFile.Close()

	carStore, err := storage.NewWritable(carFile, roots)
	if err != nil {
		return nil, fmt.Errorf("cannot open writable car storage: %w", err)
	}

	if err = carStore.Put(ctx, adCid.KeyString(), data); err != nil {
		return nil, &WriteError{fmt.Errorf("cannot write advertisement data to car file: %w", err)}
	}

	delCids := make([]cid.Cid, 1, len(roots))
	delCids[0] = adCid

	if entriesCid != cid.Undef {
		delCids = append(delCids, entriesCid)

		node, data, err := cw.loadNode(entriesCid)
		if err != nil {
			if !errors.Is(err, datastore.ErrNotFound) {
				cw.deleteCids(delCids)
				return nil, fmt.Errorf("failed to load first entry: %w", err)
			}
			// OK to have entries in datastore, since this may be an
			// advertisement that does not have any entries or has deleted
			// entries.
		}

		if len(data) != 0 {
			if isHAMT(node) {
				return nil, ErrHAMT
			}
			for entriesCid != cid.Undef {
				chunk, data, err := cw.loadEntryChunk(entriesCid)
				if err != nil {
					cw.deleteCids(delCids)
					return nil, fmt.Errorf("cannot load entries block: %w", err)
				}
				if err = carStore.Put(ctx, entriesCid.KeyString(), data); err != nil {
					return nil, &WriteError{fmt.Errorf("cannot write entries block to car file: %w", err)}
				}
				if chunk.Next == nil {
					break
				}
				entriesCid = chunk.Next.(cidlink.Link).Cid
				delCids = append(delCids, entriesCid)
			}
		}
	}

	if err = carStore.Finalize(); err != nil {
		return nil, &WriteError{fmt.Errorf("cannot finalize advertisement car file: %w", err)}
	}

	_, err = carFile.Seek(0, io.SeekStart)
	if err != nil {
		return nil, &WriteError{err}
	}

	if cw.compAlg == Gzip {
		gzTmpName := carTmpName + GzipFileSuffix
		gzFile, err := os.Create(gzTmpName)
		if err != nil {
			return nil, &WriteError{fmt.Errorf("cannot create gzip file: %w", err)}
		}
		defer os.Remove(gzTmpName)
		defer gzFile.Close()

		wbuf := bufio.NewWriter(gzFile)
		gzw := gzip.NewWriter(wbuf)
		_, err = io.Copy(gzw, carFile)
		if err != nil {
			return nil, &WriteError{fmt.Errorf("cannot write gzip file: %w", err)}
		}
		if err = carFile.Close(); err != nil {
			// Since data in car file has already been written, an error from close
			// is not critical, so only log warning.
			log.Warnw("Error closing temporary car file", "err", err, "name", carFile.Name())
		}
		// Close gzip writer; finish writing gzip data to buffer.
		if err = gzw.Close(); err != nil {
			return nil, &WriteError{fmt.Errorf("cannot close gzip writer: %w", err)}
		}
		// Flush buffered data to file.
		if err = wbuf.Flush(); err != nil {
			return nil, &WriteError{fmt.Errorf("cannot write gzip file: %w", err)}
		}
		_, err = gzFile.Seek(0, io.SeekStart)
		if err != nil {
			return nil, &WriteError{err}
		}
		carFile = gzFile
	}

	carInfo, err := cw.fileStore.Put(ctx, carPath, carFile)
	if err != nil {
		return nil, &WriteError{err}
	}

	if err = carFile.Close(); err != nil {
		// Since data in car file has already been written, an error from close
		// is not critical, so only log warning.
		log.Warnw("Error closing temporary car file", "err", err, "name", carFile.Name())
	}

	cw.deleteCids(delCids)
	return carInfo, nil
}

// WriteChain reads the advertisement, specified by CID, from the datastore and
// writes it and its entries into a CAR file. If the advertisement has a
// previous advertisement, then it is written also. This continues until there
// are no more previous advertisements in the datastore or until an
// advertisement does not have a previous.
func (cw *CarWriter) WriteChain(ctx context.Context, adCid cid.Cid) (int, error) {
	rmCtxID := make(map[string]struct{})
	var count int

	for adCid != cid.Undef {
		ad, data, err := cw.loadAd(ctx, adCid)
		if err != nil {
			if !errors.Is(err, datastore.ErrNotFound) {
				return 0, fmt.Errorf("cannot load advertisement: %w", err)
			}
			break
		}

		ctxIdStr := string(ad.ContextID)
		_, skipEnts := rmCtxID[ctxIdStr]

		_, err = cw.write(ctx, adCid, ad, data, skipEnts)
		if err != nil {
			return 0, err
		}
		count++

		// If this is a remove, skip all earlier ads with the same context ID.
		if ad.IsRm {
			rmCtxID[ctxIdStr] = struct{}{}
		}

		if ad.PreviousID == nil {
			break
		}
		adCid = ad.PreviousID.(cidlink.Link).Cid
	}

	return count, nil
}

// WriteExisting iterates the datastore to find existing advertisements. It
// then writes these and their entries to CAR files. Advertisements and entries
// that are written to CAR files are removed from the datastore.
//
// An error writing to a CAR file, or context cancellation, stops processing
// the advertisements from the datastore.
func (cw *CarWriter) WriteExisting(ctx context.Context) error {
	var q query.Query
	results, err := cw.dstore.Query(ctx, q)
	if err != nil {
		return err
	}
	defer results.Close()

	var count int
	for result := range results.Next() {
		if result.Error != nil {
			return fmt.Errorf("cannot read query result from datastore: %w", result.Error)
		}
		ent := result.Entry
		key := ent.Key[1:]

		// Not a CID if it contains "/".
		if strings.Contains(key, "/") {
			continue
		}
		if len(ent.Value) == 0 {
			continue
		}
		adCid, err := cid.Decode(key)
		if err != nil {
			continue
		}
		node, err := decodeIPLDNode(bytes.NewBuffer(ent.Value), adCid.Prefix().Codec, schema.AdvertisementPrototype)
		if err != nil {
			continue
		}
		if isAdvertisement(node) {
			_, err = cw.Write(ctx, adCid, false)
			if err != nil {
				log.Errorw("Cannot write saved advertisement to CAR file", "err", err, "adCid", adCid)
				var werr *WriteError
				if errors.As(err, &werr) {
					// Error writing to car file; stop writing ads.
					break
				}
				// Log error, but keep going.
				continue
			}
			count++
		}
	}
	log.Infow("Wrote saved advertisements from datastore to CAR files", "count", count)

	return nil
}

func (cw *CarWriter) WriteHead(ctx context.Context, adCid cid.Cid, publisher peer.ID) (*filestore.File, error) {
	err := publisher.Validate()
	if err != nil {
		return nil, err
	}

	headName := publisher.String() + HeadFileSuffix
	return cw.fileStore.Put(ctx, headName, strings.NewReader(adCid.String()))
}

func (cw *CarWriter) deleteCids(delCids []cid.Cid) {
	for i := len(delCids) - 1; i >= 0; i-- {
		err := cw.dstore.Delete(context.Background(), datastore.NewKey(delCids[i].String()))
		if err != nil {
			log.Errorw("Error deleting advertisement data from datastore", "err", err)
		}
	}
}

func (cw *CarWriter) loadAd(ctx context.Context, c cid.Cid) (schema.Advertisement, []byte, error) {
	data, err := cw.dstore.Get(ctx, datastore.NewKey(c.String()))
	if err != nil {
		return schema.Advertisement{}, nil, err
	}
	ad, err := decodeAd(data, c)
	if err != nil {
		return schema.Advertisement{}, nil, err
	}
	return ad, data, nil
}

func (cw *CarWriter) loadEntryChunk(c cid.Cid) (*schema.EntryChunk, []byte, error) {
	data, err := cw.dstore.Get(context.Background(), datastore.NewKey(c.String()))
	if err != nil {
		return nil, nil, err
	}
	chunk, err := decodeEntryChunk(data, c)
	if err != nil {
		return nil, nil, err
	}
	return chunk, data, nil
}

func (cw *CarWriter) loadNode(c cid.Cid) (ipld.Node, []byte, error) {
	val, err := cw.dstore.Get(context.Background(), datastore.NewKey(c.String()))
	if err != nil {
		return nil, nil, err
	}
	node, err := decodeIPLDNode(bytes.NewBuffer(val), c.Prefix().Codec, basicnode.Prototype.Any)
	if err != nil {
		return nil, nil, err
	}
	return node, val, nil
}

func (cw *CarWriter) removeAdData(delCids []cid.Cid) error {
	defer func() {
		cw.deleteCids(delCids)
	}()

	if len(delCids) < 2 {
		return nil
	}

	entriesCid := delCids[1]

	node, _, err := cw.loadNode(entriesCid)
	if err != nil {
		if !errors.Is(err, datastore.ErrNotFound) {
			return fmt.Errorf("failed to load first entry: %w", err)
		}
		return nil // advertisement has no entries
	}

	if isHAMT(node) {
		return nil
	}

	for entriesCid != cid.Undef {
		chunk, _, err := cw.loadEntryChunk(entriesCid)
		if err != nil {
			return fmt.Errorf("cannot load entries block: %w", err)
		}
		if chunk.Next == nil {
			break
		}
		entriesCid = chunk.Next.(cidlink.Link).Cid
		delCids = append(delCids, entriesCid)
	}

	return nil
}

func decodeAd(data []byte, c cid.Cid) (schema.Advertisement, error) {
	node, err := decodeIPLDNode(bytes.NewBuffer(data), c.Prefix().Codec, schema.AdvertisementPrototype)
	if err != nil {
		return schema.Advertisement{}, fmt.Errorf("failed to decode ipldNode: %w", err)
	}
	ad, err := schema.UnwrapAdvertisement(node)
	if err != nil {
		return schema.Advertisement{}, fmt.Errorf("cannot decode advertisement: %w", err)
	}
	return *ad, nil
}

func decodeEntryChunk(data []byte, c cid.Cid) (*schema.EntryChunk, error) {
	node, err := decodeIPLDNode(bytes.NewBuffer(data), c.Prefix().Codec, schema.EntryChunkPrototype)
	if err != nil {
		return nil, fmt.Errorf("failed to decode ipldNode: %w", err)
	}
	chunk, err := schema.UnwrapEntryChunk(node)
	if err != nil {
		return nil, fmt.Errorf("cannot decode entries chunk: %w", err)
	}
	return chunk, nil
}

// decodeIPLDNode decodes an ipld.Node from bytes read from an io.Reader.
func decodeIPLDNode(r io.Reader, codec uint64, prototype ipld.NodePrototype) (ipld.Node, error) {
	nb := prototype.NewBuilder()
	decoder, err := multicodec.LookupDecoder(codec)
	if err != nil {
		return nil, err
	}
	if err = decoder(nb, r); err != nil {
		return nil, err
	}
	return nb.Build(), nil
}

// isAdvertisement checks if an IPLD node is an advertisement, by looking to
// see if it has a "Signature" field. Additional checks may be needed if the
// schema is extended with new types that are traversable.
func isAdvertisement(node ipld.Node) bool {
	indexID, _ := node.LookupByString("Signature")
	return indexID != nil
}

// isHAMT checks if the given IPLD node is a HAMT root node by looking for a
// field named "hamt".
//
// See: https://github.com/ipld/go-ipld-adl-hamt/blob/master/schema.ipldsch
func isHAMT(n ipld.Node) bool {
	h, _ := n.LookupByString("hamt")
	return h != nil
}
