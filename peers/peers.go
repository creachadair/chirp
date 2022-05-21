// Package peers provides support code for managing and testing peers.
package peers

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/creachadair/chirp"
	"github.com/creachadair/chirp/channel"
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
func NewLocal() *Local {
	a2b, b2a := channel.Direct()
	return &Local{
		A: new(chirp.Peer).Start(a2b),
		B: new(chirp.Peer).Start(b2a),
	}
}

type Accepter interface {
	Accept(context.Context) (chirp.Channel, error)
}

// Loop accepts connections from acc and starts a peer for each one in a
// goroutine. Loop continues until acc closes or ctx ends.
//
// When ctx terminates, all running peers are stopped. When acc closes, the
// loop waits for running peers to exit before returning.
func Loop(ctx context.Context, acc Accepter, newPeer func() *chirp.Peer) error {
	var wg sync.WaitGroup
	for {
		ch, err := acc.Accept(ctx)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				err = nil
			}
			wg.Wait()
			return err
		}

		pool := sync.Pool{New: func() any { return newPeer() }}

		wg.Add(1)
		go func() {
			defer wg.Done()

			sctx, cancel := context.WithCancel(ctx)
			defer cancel()

			peer := pool.Get().(*chirp.Peer).Start(ch)
			defer pool.Put(peer)
			go func() { <-sctx.Done(); peer.Stop() }()

			peer.Wait()
		}()
	}
}

// NetAccepter adapts a net.Listener to the Accepter interface.
func NetAccepter(lst net.Listener) Accepter {
	return netAccepter{Listener: lst}
}

type netAccepter struct {
	net.Listener
}

func (n netAccepter) Accept(ctx context.Context) (chirp.Channel, error) {
	// A net.Listener does not obey a context, so simulate it by closing the
	// listener if ctx ends. The ok channel allows the context watcher to clean
	// up when we return before ctx ends.
	ok := make(chan struct{})
	defer close(ok)
	go func() {
		select {
		case <-ctx.Done():
			n.Listener.Close()
		case <-ok:
			return // release the waiter
		}
	}()

	conn, err := n.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return channel.IO(conn, conn), nil
}
