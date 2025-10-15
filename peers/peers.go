// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

// Package peers provides support code for managing and testing peers.
package peers

import (
	"context"
	"errors"
	"net"

	"github.com/creachadair/chirp"
	"github.com/creachadair/chirp/channel"
	"github.com/creachadair/taskgroup"
)

// Local is a pair of in-memory connected peers, suitable for testing.
type Local struct {
	A *chirp.Peer
	B *chirp.Peer
}

// Stop shuts down both the peers and blocks until both have exited.
func (p *Local) Stop() error {
	aerr := p.A.Stop()
	berr := p.B.Stop()
	if aerr != nil {
		return aerr
	}
	return berr
}

// NewLocal creates a pair of in-memory connected peers, that communicate via a
// direct channel without encoding.
//
// The peers in a new local instance are empty and have detached metrics.
func NewLocal() *Local {
	a2b, b2a := channel.Direct()
	return &Local{
		A: new(chirp.Peer).Detach().Start(a2b),
		B: new(chirp.Peer).Detach().Start(b2a),
	}
}

type Accepter interface {
	Accept(context.Context) (chirp.Channel, error)
}

// Loop accepts connections from acc and starts a clone of peer for each one in
// a goroutine. Changes to peer while Loop executes will affect connections
// accepted after each change is made, but peer itself is not started unless
// the caller does so explicitly.
//
// Loop runs until acc closes or ctx ends.  When ctx terminates, all running
// peers are stopped. When acc closes, Loop waits for running peers to exit
// before returning.
func Loop(ctx context.Context, acc Accepter, peer *chirp.Peer) error {
	var g taskgroup.Group
	for {
		ch, err := acc.Accept(ctx)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				err = nil
			}
			g.Wait()
			return err
		}

		g.Go(func() error {
			cp := peer.Clone().Start(ch)

			// If ctx ends, stop the peer. Clean up the stop function if the peer
			// ends before ctx, however, since ctx may run for a long time, and we
			// do not want dead stop callbacks to pile up.
			done := context.AfterFunc(ctx, func() { cp.Stop() })
			defer done()

			return cp.Wait()
		})
	}
}

// NetAccepter adapts a [net.Listener] to the [Accepter] interface.
func NetAccepter(lst net.Listener) Accepter {
	return netAccepter{Listener: lst}
}

type netAccepter struct {
	net.Listener
}

func (n netAccepter) Accept(ctx context.Context) (chirp.Channel, error) {
	// A net.Listener does not obey a context, so simulate it by closing the
	// listener if ctx ends.
	stop := context.AfterFunc(ctx, func() { n.Listener.Close() })
	conn, err := n.Listener.Accept()
	stop()
	if err != nil {
		return nil, err
	}
	return channel.IO(conn, conn), nil
}
