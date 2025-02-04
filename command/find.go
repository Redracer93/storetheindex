package command

import (
	"encoding/base64"
	"fmt"

	"github.com/ipfs/go-cid"
	"github.com/ipni/go-libipni/find/client"
	httpclient "github.com/ipni/go-libipni/find/client/http"
	p2pclient "github.com/ipni/go-libipni/find/client/p2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multihash"
	"github.com/urfave/cli/v2"
)

var FindCmd = &cli.Command{
	Name:   "find",
	Usage:  "Find value by CID or multihash in indexer",
	Flags:  findFlags,
	Action: findAction,
}

var findFlags = []cli.Flag{
	&cli.StringSliceFlag{
		Name:     "mh",
		Usage:    "Specify multihash to use as indexer key, multiple OK",
		Required: false,
	},
	&cli.StringSliceFlag{
		Name:     "cid",
		Usage:    "Specify CID to use as indexer key, multiple OK",
		Required: false,
	},
	indexerHostFlag,
	&cli.StringFlag{
		Name:     "indexerid",
		Usage:    "Indexer peer ID to use when protocol=libp2p",
		Aliases:  []string{"iid"},
		EnvVars:  []string{"INDEXER_ID"},
		Required: false,
	},
	&cli.StringFlag{
		Name:     "protocol",
		Usage:    "Protocol to query the indexer (http, libp2p currently supported)",
		Value:    "http",
		Required: false,
	},
}

func findAction(cctx *cli.Context) error {
	protocol := cctx.String("protocol")

	mhArgs := cctx.StringSlice("mh")
	cidArgs := cctx.StringSlice("cid")
	mhs := make([]multihash.Multihash, 0, len(mhArgs)+len(cidArgs))
	for i := range mhArgs {
		m, err := multihash.FromB58String(mhArgs[i])
		if err != nil {
			return err
		}
		mhs = append(mhs, m)
	}
	for i := range cidArgs {
		c, err := cid.Decode(cidArgs[i])
		if err != nil {
			return err
		}
		mhs = append(mhs, c.Hash())
	}

	var cl client.Interface
	var err error

	switch protocol {
	case "http":
		cl, err = httpclient.New(cliIndexer(cctx, "find"))
		if err != nil {
			return err
		}
	case "libp2p":
		peerID, err := peer.Decode(cctx.String("peerid"))
		if err != nil {
			return err
		}

		c, err := p2pclient.New(nil, peerID)
		if err != nil {
			return err
		}

		err = c.Connect(cctx.Context, cliIndexer(cctx, "find"))
		if err != nil {
			return err
		}
		cl = c
	default:
		return fmt.Errorf("unrecognized protocol type for client interaction: %s", protocol)
	}

	resp, err := cl.FindBatch(cctx.Context, mhs)
	if err != nil {
		return err
	}

	if len(resp.MultihashResults) == 0 {
		fmt.Println("index not found")
		return nil
	}

	fmt.Println("Content providers:")
	for i := range resp.MultihashResults {
		fmt.Println("   Multihash:", resp.MultihashResults[i].Multihash.B58String(), "==>")
		for _, pr := range resp.MultihashResults[i].ProviderResults {
			fmt.Println("       Provider:", pr.Provider)
			fmt.Println("       ContextID:", base64.StdEncoding.EncodeToString(pr.ContextID))
			fmt.Println("       Metadata:", base64.StdEncoding.EncodeToString(pr.Metadata))
		}
	}
	return nil
}
