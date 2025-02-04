package dynmux

import (
	"context"

	"github.com/brendoncarroll/go-p2p"
	"github.com/brendoncarroll/go-p2p/s/swarmutil"
)

type baseSwarm struct {
	m    *muxer
	name string

	tellHub *swarmutil.TellHub
	askHub  *swarmutil.AskHub
}

func newSwarm(m *muxer, name string) *baseSwarm {
	return &baseSwarm{
		m:       m,
		name:    name,
		tellHub: swarmutil.NewTellHub(),
		askHub:  swarmutil.NewAskHub(),
	}
}

func (s *baseSwarm) ServeTells(fn p2p.TellHandler) error {
	return s.tellHub.ServeTells(fn)
}

func (s *baseSwarm) ServeAsks(fn p2p.AskHandler) error {
	if _, ok := s.m.s.(p2p.Asker); !ok {
		panic("underlying swarm does not support ask")
	}
	return s.askHub.ServeAsks(fn)
}

func (s *baseSwarm) Tell(ctx context.Context, addr p2p.Addr, data p2p.IOVec) error {
	i, err := s.m.lookup(ctx, addr, s.name)
	if err != nil {
		return err
	}
	msg := Message{}
	msg.SetChannel(i)
	msg.SetData(p2p.VecBytes(data))
	return s.m.s.Tell(ctx, addr, p2p.IOVec{msg})
}

func (s *baseSwarm) Ask(ctx context.Context, addr p2p.Addr, data p2p.IOVec) ([]byte, error) {
	innerSwarm := s.m.s.(p2p.AskSwarm)
	i, err := s.m.lookup(ctx, addr, s.name)
	if err != nil {
		return nil, err
	}
	msg := Message{}
	msg.SetChannel(i)
	msg.SetData(p2p.VecBytes(data))
	return innerSwarm.Ask(ctx, addr, p2p.IOVec{msg})
}

func (s *baseSwarm) MTU(ctx context.Context, addr p2p.Addr) int {
	return s.m.s.MTU(ctx, addr) - channelSize
}

func (s *baseSwarm) LocalAddrs() []p2p.Addr {
	return s.m.s.LocalAddrs()
}

func (s *baseSwarm) Close() error {
	s.askHub.CloseWithError(p2p.ErrSwarmClosed)
	s.tellHub.CloseWithError(p2p.ErrSwarmClosed)
	return nil
}

func (s *baseSwarm) ParseAddr(data []byte) (p2p.Addr, error) {
	return s.m.s.ParseAddr(data)
}
