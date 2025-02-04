package p2pfindserver

import (
	"context"

	indexer "github.com/ipni/go-indexer-core"
	"github.com/ipni/storetheindex/internal/counter"
	"github.com/ipni/storetheindex/internal/libp2pserver"
	"github.com/ipni/storetheindex/internal/registry"
	"github.com/libp2p/go-libp2p/core/host"
)

type FindServer struct {
	libp2pserver.Server
	p2pHandler *libp2pHandler
}

// New creates a new libp2p server
func New(ctx context.Context, h host.Host, indexer indexer.Interface, registry *registry.Registry, indexCounts *counter.IndexCounts) *FindServer {
	p2ph := newHandler(indexer, registry, indexCounts)
	s := &FindServer{
		p2pHandler: p2ph,
	}
	s.Server = *libp2pserver.New(ctx, h, p2ph)
	return s
}

func (s *FindServer) RefreshStats() {
	s.p2pHandler.findHandler.RefreshStats()
}
