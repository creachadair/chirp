// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

package peers_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/creachadair/chirp"
	"github.com/creachadair/chirp/channel"
	"github.com/creachadair/chirp/peers"
	"github.com/creachadair/taskgroup"
	"github.com/fortytw2/leaktest"
)

func TestLoop(t *testing.T) {
	defer leaktest.Check(t)()

	lst, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer lst.Close()
	addr := lst.Addr().String()

	t.Logf("Listening at %q", addr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	base := chirp.NewPeer().Handle("100", slowEcho)
	loop := taskgroup.Go(func() error {
		return peers.Loop(ctx, peers.NetAccepter(lst), base)
	})
	t.Logf("Started peer loop...")

	const numClients = 5
	const numCalls = 5

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
				_, err := peer.Call(context.Background(), "100", nil)
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
	time.Sleep(10 * time.Millisecond)
	return req.Data, nil
}
