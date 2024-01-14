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

	loop := taskgroup.Go(func() error {
		return peers.Loop(ctx, peers.NetAccepter(lst), func() *chirp.Peer {
			return chirp.NewPeer().Handle("100", slowEcho)
		})
	})
	t.Logf("Started peer loop...")

	const numClients = 5
	const numCalls = 5

	g := taskgroup.New(taskgroup.Listen(func(err error) {
		cancel()
		t.Errorf("Task error: %v", err)
	}))
	for i := 0; i < numClients; i++ {
		g.Go(func() error {
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				return err
			}
			defer conn.Close()
			peer := chirp.NewPeer().Start(channel.IO(conn, conn))
			for j := 0; j < numCalls; j++ {
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

func TestHandlerMap(t *testing.T) {
	defer leaktest.Check(t)()

	m := peers.HandlerMap{
		"name": func(ctx context.Context, r *chirp.Request) ([]byte, error) {
			return []byte(r.Method), nil
		},
		"echo": func(ctx context.Context, r *chirp.Request) ([]byte, error) {
			return r.Data, nil
		},
		"none": nil, // has the effect of unregistering this name
	}
	ctx := context.Background()
	checkCall := func(t *testing.T, p *chirp.Peer, method, arg, want string) {
		rsp, err := p.Call(ctx, method, []byte(arg))
		if err != nil {
			t.Fatalf("Call %q unexpectedly failed; %v", method, err)
		} else if got := string(rsp.Data); got != want {
			t.Fatalf("Call %q: got %v %q, want %q", method, rsp, got, want)
		}
	}
	checkFail := func(t *testing.T, p *chirp.Peer, method, arg string) {
		if rsp, err := p.Call(ctx, method, []byte(arg)); err == nil {
			t.Fatalf("Call %q: got %v, want error", method, rsp)
		}
	}
	hello := func(context.Context, *chirp.Request) ([]byte, error) { return []byte("hello"), nil }

	t.Run("Register", func(t *testing.T) {
		loc := peers.NewLocal()
		defer loc.Stop()

		// Register "none" as a method name, so we can check that it is replaced
		// when the map is applied.
		loc.A.Handle("none", hello)
		checkCall(t, loc.B, "none", "", "hello")

		m.Register(loc.A)
		checkCall(t, loc.B, "name", "", "name")
		checkCall(t, loc.B, "echo", "ok", "ok")
		checkFail(t, loc.B, "none", "")             // now unavailable
		checkFail(t, loc.B, "nonesuch", "whatever") // always unavailable
	})

	t.Run("RegisterWithPrefix", func(t *testing.T) {
		loc := peers.NewLocal()
		defer loc.Stop()

		// Register "none" as a method name, so we can check that it is not
		// replaced when the map is applied with a prefix.
		// Register "pfx.none" so we can check that it is removed.
		loc.A.Handle("none", hello)
		loc.A.Handle("pfx.none", hello)
		checkCall(t, loc.B, "none", "", "hello")
		checkCall(t, loc.B, "pfx.none", "", "hello")

		m.RegisterWithPrefix(loc.A, "pfx.")

		// The unprefixed names should not be present.
		checkFail(t, loc.B, "name", "")
		checkFail(t, loc.B, "echo", "ok")
		checkFail(t, loc.B, "nonesuch", "whatever")

		// The prefixed names should respond correctly.
		checkCall(t, loc.B, "pfx.name", "", "pfx.name")
		checkCall(t, loc.B, "pfx.echo", "ok", "ok")

		// The prefixed "pfx.none" method should have been removed.
		checkFail(t, loc.B, "pfx.none", "")

		// The unprefixed "none" method should not have been removed.
		checkCall(t, loc.B, "none", "", "hello")
	})
}
