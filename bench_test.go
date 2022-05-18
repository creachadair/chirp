package chirp_test

import (
	"context"
	"io"
	"testing"

	"github.com/creachadair/chirp"
	"github.com/creachadair/chirp/channel"
	"github.com/creachadair/chirp/peers"
)

func noop(context.Context, *chirp.Request) ([]byte, error) { return nil, nil }

func BenchmarkCall(b *testing.B) {
	b.Run("Direct", func(b *testing.B) {
		loc := peers.NewLocal()
		defer loc.Stop()

		loc.A.Handle(1, noop)
		runBench(b, loc.B)
	})

	b.Run("IO", func(b *testing.B) {
		ar, bw := io.Pipe()
		br, aw := io.Pipe()
		pa := chirp.NewPeer().Start(channel.IO(ar, aw)).Handle(1, noop)
		pb := chirp.NewPeer().Start(channel.IO(br, bw))
		defer func() {
			if err := pa.Stop(); err != nil {
				b.Errorf("A stop: %v", err)
			}
			if err := pb.Stop(); err != nil {
				b.Errorf("B stop: %v", err)
			}
		}()
		runBench(b, pb)
	})
}

func runBench(b *testing.B, peer *chirp.Peer) {
	b.Helper()
	ctx := context.Background()

	for i := 0; i < b.N; i++ {
		_, err := peer.Call(ctx, 1, nil)
		if err != nil {
			b.Fatal(err)
		}
	}
}
