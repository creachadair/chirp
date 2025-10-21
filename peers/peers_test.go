// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

package peers_test

import (
	"context"
	mrand "math/rand/v2"
	"testing"
	"testing/synctest"
	"time"

	"github.com/creachadair/chirp"
	"github.com/creachadair/chirp/channel"
	"github.com/creachadair/chirp/peers"
	"github.com/creachadair/mds/mnet"
	"github.com/creachadair/taskgroup"
)

func newTestNetwork(t *testing.T) *mnet.Network {
	n := mnet.New(t.Name() + " network")
	t.Cleanup(func() { n.Close() })
	return n
}

func mustListen(t *testing.T, n *mnet.Network) mnet.Listener {
	t.Helper()
	lst := n.MustListen("test", t.Name())
	t.Logf("Listening at %q %q", lst.Addr().Network(), lst.Addr().String())
	return lst
}

func TestAccepter(t *testing.T) {
	t.Run("OK", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			n := newTestNetwork(t)
			lst := mustListen(t, n)
			acc := peers.NetAccepter(lst)

			time.AfterFunc(1*time.Second, func() {
				conn, err := lst.DialContext(t.Context())
				if err != nil {
					t.Errorf("Dial failed: %v", err)
				}
				t.Logf("Dial OK: %v", conn.RemoteAddr())
				conn.Close()
			})

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
			n := newTestNetwork(t)
			lst := mustListen(t, n)
			acc := peers.NetAccepter(lst)

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			ch, err := acc.Accept(ctx)
			if err == nil {
				t.Errorf("Accept: got %v, want error", ch)
			}
		})
	})
}

func TestLoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := newTestNetwork(t)
		lst := mustListen(t, n)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		base := chirp.NewPeer().Handle("100", slowEcho)
		loop := taskgroup.Go(func() error {
			return peers.Loop(ctx, peers.NetAccepter(lst), base.Clone)
		})
		t.Log("Started peer loop...")

		const numClients = 10
		const numCalls = 100
		t.Logf("Clients: %d, calls per client: %d", numClients, numCalls)

		g := taskgroup.New(func(err error) {
			cancel()
			t.Errorf("Task error: %v", err)
		})
		for range numClients {
			g.Go(func() error {
				conn, err := lst.Dial()
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
	})
}

func slowEcho(ctx context.Context, req *chirp.Request) ([]byte, error) {
	time.Sleep((mrand.N[time.Duration](500) + 500) * time.Millisecond)
	return req.Data, nil
}
