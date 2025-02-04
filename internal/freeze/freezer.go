package freeze

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/ipfs/go-datastore"
	logging "github.com/ipfs/go-log/v2"
	"github.com/ipni/storetheindex/fsutil/disk"
)

var log = logging.Logger("indexer/freezer")

const (
	frozenKey = "/freeze/frozen"

	maxCheckInterval = time.Hour
	minCheckInterval = 30 * time.Second

	// logAlertRemaining is the percent from the freeze threshold at which to
	// log a disk usage alert.
	logAlertRemaining = 10.0
	// logCriticalRemaining is the percent from the freeze threshold at which
	// to log that disk usage is critical.
	logCriticalRemaining = 2.0
)

type Freezer struct {
	checkNow    chan chan struct{}
	done        chan struct{}
	dstore      datastore.Datastore
	freezeAt    float64
	freezeAtStr string
	freezeFunc  func() error
	frozen      chan struct{}
	trigger     chan struct{}
	triggerErr  chan error
	path        string
}

// New creates a new Freezer that checks the usage of the file system at dirPath.
func New(dirPath string, freezeAtPercent float64, dstore datastore.Datastore, freezeFunc func() error) (*Freezer, error) {
	f := &Freezer{
		dstore:      dstore,
		freezeAt:    freezeAtPercent,
		freezeAtStr: fmt.Sprintf("%s%%", strconv.FormatFloat(freezeAtPercent, 'f', -1, 64)),
		freezeFunc:  freezeFunc,
		frozen:      make(chan struct{}),
		path:        dirPath,
	}
	frozen, err := f.loadFrozenState()
	if err != nil {
		return nil, err
	}

	if frozen {
		log.Info("Indexer already frozen")
		return f, nil
	}

	// If not frozen, check disk usage and start monitor.
	nextCheck, frozen, err := f.check()
	if err != nil {
		return nil, err
	}
	if !frozen {
		// Start disk usage monitor.
		f.checkNow = make(chan chan struct{})
		f.done = make(chan struct{})
		f.trigger = make(chan struct{})
		f.triggerErr = make(chan error)
		go f.run(nextCheck)
	}

	return f, nil
}

// Freeze manually triggers the indexer to enter frozen mode.
func (f *Freezer) Freeze() error {
	select {
	case f.trigger <- struct{}{}:
		return <-f.triggerErr
	case <-f.frozen:
	}
	return nil
}

// Frozen returns true if indexer is frozen.
func (f *Freezer) Frozen() bool {
	select {
	case <-f.frozen:
		return true
	default:
	}
	return false
}

// CheckNow triggers an immediate disk usage check.
func (f *Freezer) CheckNow() bool {
	if f.Frozen() {
		return true
	}
	checkDone := make(chan struct{})
	select {
	case f.checkNow <- checkDone:
		<-checkDone
		return false
	case <-f.frozen:
	}
	return true
}

// Close stops the goroutine that checks disk usage.
func (f *Freezer) Close() {
	if f == nil || f.checkNow == nil {
		return
	}
	close(f.checkNow)
	<-f.done
	f.checkNow = nil
}

func (f *Freezer) Usage() (*disk.UsageStats, error) {
	du, err := disk.Usage(f.path)
	if err != nil {
		return nil, fmt.Errorf("cannot get disk usage at path %q: %w", f.path, err)
	}
	return du, nil
}

func Unfreeze(ctx context.Context, dirPath string, freezeAtPercent float64, dstore datastore.Datastore) error {
	if dirPath == "" || dstore == nil {
		return nil
	}
	frozen, err := dstore.Has(ctx, datastore.NewKey(frozenKey))
	if err != nil {
		return err
	}
	if !frozen {
		return nil
	}

	du, err := disk.Usage(dirPath)
	if err != nil {
		return fmt.Errorf("cannot get disk usage for freeze check at path %q: %w", dirPath, err)
	}
	if du.Percent >= freezeAtPercent {
		return fmt.Errorf("cannot unfreeze: disk usage above %f", freezeAtPercent)
	}

	dsKey := datastore.NewKey(frozenKey)
	if err = dstore.Delete(ctx, dsKey); err != nil {
		return err
	}
	return dstore.Sync(ctx, dsKey)
}

// run periodically check file system usage and sets the frozen state if the
// usage reaches the freeze-at point.
func (f *Freezer) run(nextCheck time.Duration) {
	defer close(f.done)

	timer := time.NewTimer(nextCheck)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			nextCheck, frozen, err := f.check()
			if err != nil {
				log.Error(err)
				nextCheck = minCheckInterval
			}
			if frozen {
				return
			}
			if nextCheck != 0 {
				timer.Reset(nextCheck)
			}
		case checkDone, open := <-f.checkNow:
			if !open {
				return
			}
			_, frozen, err := f.check()
			if err != nil {
				log.Error(err)
			}
			if frozen {
				return
			}
			checkDone <- struct{}{}
		case <-f.trigger:
			log.Info("Freeze administratively triggered")
			f.triggerErr <- f.freeze()
			close(f.triggerErr)
			return
		}
	}
}

// check examines the file system to see if the usage is at the freeze-at
// point. If the freeze-at point is reached, then the frozen state becomes
// true. The frozen state is persisted so a new Freezer will start frozen.
func (f *Freezer) check() (time.Duration, bool, error) {
	if f.path == "" {
		return 0, false, nil
	}

	du, err := disk.Usage(f.path)
	if err != nil {
		return 0, false, fmt.Errorf("cannot get disk usage for freeze check at path %q: %w", f.path, err)
	}
	if du.Percent >= f.freezeAt {
		err = f.freeze()
		if err != nil {
			return 0, false, err
		}
		return 0, true, nil
	}

	log := log.With("usage", fmt.Sprintf("%.2f%%", du.Percent), "freezeAt", f.freezeAtStr)
	if du.Percent >= f.freezeAt-logAlertRemaining {
		if du.Percent >= f.freezeAt-logCriticalRemaining {
			log.Warnw("Disk usage CRITICAL")
		} else {
			log.Warnw("Disk usage ALERT")
		}
	} else {
		log.Infow("Disk usage OK")
	}

	// Next check interval is proportional to the storage remaining until
	// reaching the freeze-at point.
	nextCheck := time.Duration(float64(maxCheckInterval) * (f.freezeAt - du.Percent) / 100.0)
	if nextCheck < minCheckInterval {
		nextCheck = minCheckInterval
	}
	return nextCheck, false, nil
}

func (f *Freezer) freeze() error {
	if f.freezeFunc != nil {
		if err := f.freezeFunc(); err != nil {
			return err
		}
	}

	if f.dstore != nil {
		ctx := context.Background()
		dsKey := datastore.NewKey(frozenKey)
		value := time.Now().Format(time.RFC3339)
		err := f.dstore.Put(ctx, dsKey, []byte(value))
		if err != nil {
			return err
		}
		if err = f.dstore.Sync(ctx, dsKey); err != nil {
			return err
		}
	}

	close(f.frozen)
	log.Warn("Indexer frozen")
	return nil
}

func (f *Freezer) loadFrozenState() (bool, error) {
	if f.dstore != nil {
		frozen, err := f.dstore.Has(context.Background(), datastore.NewKey(frozenKey))
		if err != nil {
			return false, err
		}
		if frozen {
			close(f.frozen)
			return true, nil
		}
	}
	return false, nil
}
