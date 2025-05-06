// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

package chirp_test

import (
	"context"
	"io"
	"testing"

	"github.com/creachadair/chirp"
	"github.com/creachadair/chirp/channel"
	"github.com/creachadair/chirp/peers"
)

func noop(context.Context, *chirp.Request) ([]byte, error)       { return nil, nil }
func echo(_ context.Context, req *chirp.Request) ([]byte, error) { return req.Data, nil }

func BenchmarkCall(b *testing.B) {
	var payload = []byte("fuzzy wuzzy was a bear\nfuzzy wuzzy had no hair\nfuzzy wuzzy wasn't fuzzy was he?")

	b.Run("Direct-noop", func(b *testing.B) {
		loc := peers.NewLocal()
		defer loc.Stop()

		loc.A.Handle("X", noop)
		runBench(b, loc.B, nil)
	})
	b.Run("Direct-echo", func(b *testing.B) {
		loc := peers.NewLocal()
		defer loc.Stop()

		loc.A.Handle("X", echo)
		runBench(b, loc.B, payload)
	})

	b.Run("IO-noop", func(b *testing.B) {
		pa, pb := pipePeers(b)
		pa.Handle("X", noop)
		runBench(b, pb, nil)
	})
	b.Run("IO-echo", func(b *testing.B) {
		pa, pb := pipePeers(b)
		pa.Handle("X", echo)
		runBench(b, pb, payload)
	})
}

func runBench(b *testing.B, peer *chirp.Peer, data []byte) {
	b.Helper()
	ctx := context.Background()

	for b.Loop() {
		_, err := peer.Call(ctx, "X", nil)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func pipePeers(tb testing.TB) (pa, pb *chirp.Peer) {
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	pa = chirp.NewPeer().Start(channel.IO(ar, aw))
	pb = chirp.NewPeer().Start(channel.IO(br, bw))
	tb.Cleanup(func() {
		if err := pa.Stop(); err != nil {
			tb.Errorf("A stop: %v", err)
		}
		if err := pb.Stop(); err != nil {
			tb.Errorf("B stop: %v", err)
		}
	})
	return
}
