// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

package peers_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/creachadair/chirp"
	"github.com/creachadair/chirp/channel"
	"github.com/creachadair/chirp/peers"
	"github.com/creachadair/taskgroup"
	"github.com/fortytw2/leaktest"
)

func mustListen(t *testing.T) (_ net.Listener, addr string) {
	t.Helper()
	lst, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr = lst.Addr().String()
	t.Cleanup(func() { lst.Close() })
	t.Logf("Listening at %q", addr)
	return lst, addr
}

type fakeListener struct {
	net.Listener // stub for unused methods
	conns        chan net.Conn
	closed       chan struct{}
}

func (f fakeListener) push(c net.Conn) { f.conns <- c }

func (f fakeListener) Accept() (net.Conn, error) {
	select {
	case <-f.closed:
		return nil, net.ErrClosed
	case c := <-f.conns:
		return c, nil
	}
}

func (f fakeListener) Close() error {
	select {
	case <-f.closed:
		return net.ErrClosed
	default:
		close(f.closed)
		return nil
	}
}

func newFakeListener() fakeListener {
	return fakeListener{
		conns:  make(chan net.Conn),
		closed: make(chan struct{}),
	}
}

// fakeConn is a fake implementation of [net.Conn] that does not work but which
// satisfies the interface, for use in testing. Only the Close method can be
// called without panicking.
type fakeConn struct{ net.Conn }

func (fakeConn) Close() error { return nil }

func TestAccepter(t *testing.T) {
	t.Run("OK", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			lst := newFakeListener()
			acc := peers.NetAccepter(lst)

			time.AfterFunc(1*time.Second, func() { lst.push(fakeConn{}) })
			c, err := acc.Accept(t.Context())
			if err != nil {
				t.Fatalf("Accept: unexpected error: %v", err)
			}
			if _, ok := c.(channel.IOChannel); !ok {
				t.Errorf("Accept: got %[1]T %[1]v, want %T", c, channel.IOChannel{})
			}

			// The listener should not be closed.
			if err := lst.Close(); err != nil {
				t.Errorf("Close listener: unexpected error: %v", err)
			}
		})
	})

	t.Run("Cancel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			lst := newFakeListener()
			acc := peers.NetAccepter(lst)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			ch, err := acc.Accept(ctx)
			if err == nil {
				t.Errorf("Accept: got %v, want error", ch)
			}

			// The listener should already be closed, so this should report that error.
			if err := lst.Close(); !errors.Is(err, net.ErrClosed) {
				t.Errorf("Close listener: got %v, want %v", err, net.ErrClosed)
			}
		})
	})
}

func TestLoop(t *testing.T) {
	defer leaktest.Check(t)()

	lst, addr := mustListen(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	base := chirp.NewPeer().Handle("100", slowEcho)
	loop := taskgroup.Go(func() error {
		return peers.Loop(ctx, peers.NetAccepter(lst), base)
	})
	t.Log("Started peer loop...")

	const numClients = 5
	const numCalls = 5
	t.Logf("Clients: %d, calls per client: %d", numClients, numCalls)

	g := taskgroup.New(func(err error) {
		cancel()
		t.Errorf("Task error: %v", err)
	})
	for range numClients {
		g.Go(func() error {
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				return err
			}
			defer conn.Close()
			peer := chirp.NewPeer().Start(channel.IO(conn, conn))
			for j := range numCalls {
				_, err := peer.Call(t.Context(), "100", nil)
				if err != nil {
					t.Errorf("Call %d: %v", j+1, err)
				}
			}
			return peer.Stop()
		})
	}
	t.Logf("Clients finished, err=%v", g.Wait())
	t.Logf("Closed listener, err=%v", lst.Close())
	t.Logf("Loop exited, err=%v", loop.Wait())
}

func slowEcho(ctx context.Context, req *chirp.Request) ([]byte, error) {
	time.Sleep(7 * time.Millisecond)
	return req.Data, nil
}
