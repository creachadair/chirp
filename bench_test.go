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
		pa, pb := ioPeers(b)
		pa.Handle("X", noop)
		runBench(b, pb, nil)
	})
	b.Run("IO-echo", func(b *testing.B) {
		pa, pb := ioPeers(b)
		pa.Handle("X", echo)
		runBench(b, pb, payload)
	})

	b.Run("Pipe-noop", func(b *testing.B) {
		pa, pb := pipePeers(b)
		pa.Handle("X", noop)
		runBench(b, pb, nil)
	})
	b.Run("Pipe-echo", func(b *testing.B) {
		pa, pb := pipePeers(b)
		pa.Handle("X", echo)
		runBench(b, pb, payload)
	})
}

func runBench(b *testing.B, peer *chirp.Peer, data []byte) {
	b.Helper()
	ctx := b.Context()

	for b.Loop() {
		_, err := peer.Call(ctx, "X", nil)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func ioPeers(tb testing.TB) (pa, pb *chirp.Peer) {
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	return startPeers(tb, channel.IO(ar, aw), channel.IO(br, bw))
}

func pipePeers(tb testing.TB) (pa, pb *chirp.Peer) {
	tb.Helper()
	ca, cb, err := channel.Pipe()
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		ca.Close()
		cb.Close()
	})
	return startPeers(tb, ca, cb)
}

func startPeers(tb testing.TB, ca, cb chirp.Channel) (pa, pb *chirp.Peer) {
	pa = chirp.NewPeer().Start(ca)
	pb = chirp.NewPeer().Start(cb)
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
