package chirp_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creachadair/chirp"
	"github.com/creachadair/chirp/channel"
	"github.com/creachadair/chirp/peers"
	"github.com/creachadair/taskgroup"
	"github.com/fortytw2/leaktest"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestPeer(t *testing.T) {
	defer leaktest.Check(t)()

	loc := peers.NewLocal()
	defer func() {
		if err := loc.Stop(); err != nil {
			t.Errorf("Stopping peers: %v", err)
		}
	}()

	// The test cases send a string in the requeset that is parsed by
	// parseTestSpec (see below) to control what the handler returns.
	loc.A.Handle(100, func(ctx context.Context, req *chirp.Request) (uint32, []byte, error) {
		return parseTestSpec(ctx, string(req.Data))
	})

	tests := []struct {
		who      *chirp.Peer     // peer originating the call
		methodID uint32          // method ID to call
		input    string          // input for parseTestSpec (generates response)
		want     *chirp.Response // expected response
	}{
		{loc.B, 10, "n/a", mustResponse(t, "1 0")}, // method not found
		{loc.A, 20, "n/a", mustResponse(t, "1 0")}, // method not found

		{loc.B, 100, "tag: 1023", mustResponse(t, "0 1023")},           // tag without data
		{loc.B, 100, "ok: yay", mustResponse(t, "0 0 yay")},            // default tag plus data
		{loc.B, 100, "ok: 617 cool", mustResponse(t, "0 617 cool")},    // tag plus data
		{loc.B, 100, "error: failure", mustResponse(t, "4 0 failure")}, // ordinary error
		{loc.B, 100, "peer?", mustResponse(t, "0 0 present")},          // check context peer

		{loc.A, 100, "ok: 617 cool", mustResponse(t, "1 0")}, // method not found
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("method-%d-%s", test.methodID, test.input), func(t *testing.T) {
			ctx := context.Background()

			rsp, err := test.who.Call(ctx, test.methodID, []byte(test.input))
			if err != nil {
				ce, ok := err.(*chirp.CallError)
				if !ok {
					t.Fatalf("Call: got error %[1]T (%[1]v), want *CallError", err)
				}
				rsp = ce.Response()
			}

			// Ignore the RequestID field, which we can't correctly predict, and
			// treat nil and empty as equivalent.
			ignoreID := cmpopts.IgnoreFields(*rsp, "RequestID")
			if diff := cmp.Diff(test.want, rsp, ignoreID, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Wrong response (-want, +got):\n%s", diff)
			}
		})
	}
}

func TestCancellation(t *testing.T) {
	defer leaktest.Check(t)()

	loc := peers.NewLocal()
	defer loc.Stop()

	type packet struct {
		T chirp.PacketType
		P string
	}

	var wg sync.WaitGroup
	wg.Add(3) // there are three packets exchanged below

	var apkt []packet
	loc.A.LogPackets(func(pkt *chirp.Packet) {
		apkt = append(apkt, packet{T: pkt.Type, P: string(pkt.Payload)})
		wg.Done()
	}).Handle(300, func(ctx context.Context, _ *chirp.Request) (uint32, []byte, error) {
		<-ctx.Done()
		return 0, nil, ctx.Err()
	})

	var bpkt []packet
	loc.B.LogPackets(func(pkt *chirp.Packet) {
		bpkt = append(bpkt, packet{T: pkt.Type, P: string(pkt.Payload)})
		wg.Done()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	rsp, err := loc.B.Call(ctx, 300, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Got %+v, %v; want %v", rsp, err, context.DeadlineExceeded)
	}

	wg.Wait()

	// B should have sent a Request followed by a Cancellation.
	if diff := cmp.Diff([]packet{
		// Request(1, 300, nil)
		{T: chirp.PacketRequest, P: "\x00\x00\x00\x01\x00\x00\x01\x2c"},
		// Cancel(1)
		{T: chirp.PacketCancel, P: "\x00\x00\x00\x01"},
	}, apkt); diff != "" {
		t.Errorf("A packets (-want, +got):\n%s", diff)
	}

	// A should have replied with a cancellation Response for B's Request.
	if diff := cmp.Diff([]packet{
		// Response(1, CANCELED, 0, nil)
		{T: chirp.PacketResponse, P: "\x00\x00\x00\x01\x03"},
	}, bpkt); diff != "" {
		t.Errorf("B packets (-want, +got):\n%s", diff)
	}
}

func TestProtocolFatal(t *testing.T) {
	defer leaktest.Check(t)()

	t.Run("BadMagic", func(t *testing.T) {
		tw, ch := rawChannel()
		p := new(chirp.Peer).Start(ch)
		time.AfterFunc(time.Second, func() { p.Stop() })

		tw.Write([]byte{'C', 'P', 1, 2, 0, 0, 0, 0})
		mustErr(t, p.Wait(), "invalid protocol version")
	})

	t.Run("ShortHeader", func(t *testing.T) {
		tw, ch := rawChannel()
		p := new(chirp.Peer).Start(ch)
		time.AfterFunc(time.Second, func() { p.Stop() })

		tw.Write([]byte{'C', 'P', 0, 2, 0, 0})
		tw.Close()
		mustErr(t, p.Wait(), "short packet header")
	})

	t.Run("ShortPayload", func(t *testing.T) {
		tw, ch := rawChannel()
		p := new(chirp.Peer).Start(ch)
		time.AfterFunc(time.Second, func() { p.Stop() })

		tw.Write([]byte{'C', 'P', 0, 2, 0, 0, 0, 10, 'a', 'b', 'c', 'd'})
		tw.Close()
		mustErr(t, p.Wait(), "short payload")
	})

	t.Run("BadRequest", func(t *testing.T) {
		tw, ch := rawChannel()
		p := new(chirp.Peer).Start(ch)
		time.AfterFunc(time.Second, func() { p.Stop() })

		tw.Write([]byte{'C', 'P', 0, 2, 0, 0, 0, 1, 'X'})
		mustErr(t, p.Wait(), "short request payload")
	})
}

func TestCustomPacket(t *testing.T) {
	loc := peers.NewLocal()

	var log []*chirp.Packet
	var got []*chirp.Packet
	loc.A.
		HandlePacket(128, func(_ context.Context, pkt *chirp.Packet) error {
			got = append(got, pkt)
			return nil
		}).
		LogPackets(func(pkt *chirp.Packet) {
			log = append(log, pkt)
		})

	// Unknown packet type: Logged but discarded.
	p1 := &chirp.Packet{Type: 100, Payload: []byte("unrecognized")}

	// Registered custom packet type: Logged and "processed".
	p2 := &chirp.Packet{Type: 128, Payload: []byte("custom")}

	if err := loc.B.SendPacket(p1.Type, p1.Payload); err != nil {
		t.Fatalf("SendPacket: %v", err)
	}
	if err := loc.B.SendPacket(p2.Type, p2.Payload); err != nil {
		t.Fatalf("SendPacket: %v", err)
	}

	// Stop the peer so the callbacks settle.
	if err := loc.Stop(); err != nil {
		t.Errorf("Stop peer: %v", err)
	}

	if diff := cmp.Diff([]*chirp.Packet{p1, p2}, log); diff != "" {
		t.Errorf("Packet log (-want, +got):\n%s", diff)
	}
	if diff := cmp.Diff([]*chirp.Packet{p2}, got); diff != "" {
		t.Errorf("Custom packet (-want, +got):\n%s", diff)
	}
}

func TestConcurrency(t *testing.T) {
	t.Run("Local", func(t *testing.T) {
		defer leaktest.Check(t)()

		loc := peers.NewLocal()
		defer loc.Stop()

		loc.A.Handle(100, slowEcho)
		loc.B.Handle(200, slowEcho)

		runConcurrent(t, loc.A, loc.B)
	})

	t.Run("Pipe", func(t *testing.T) {
		defer leaktest.Check(t)()

		ar, bw := io.Pipe()
		br, aw := io.Pipe()
		pa := chirp.NewPeer().Start(channel.IO(ar, aw))
		pb := chirp.NewPeer().Start(channel.IO(br, bw))
		defer func() {
			if err := pa.Stop(); err != nil {
				t.Errorf("A stop: %v", err)
			}
			if err := pb.Stop(); err != nil {
				t.Errorf("B stop: %v", err)
			}
		}()

		pa.Handle(100, slowEcho)
		pb.Handle(200, slowEcho)

		runConcurrent(t, pa, pb)
	})
}

func runConcurrent(t *testing.T, pa, pb *chirp.Peer) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// To give the race detector something to push against, make the peers call
	// each other lots of times concurrently and wait for the responses.
	const numCalls = 128 // per peer

	calls := taskgroup.New(taskgroup.Trigger(cancel))
	for i := 0; i < numCalls; i++ {

		// Send calls from A to B.
		ab := fmt.Sprintf("ab-call-%d", i+1)
		calls.Go(func() error {
			rsp, err := pa.Call(ctx, 200, []byte(ab))
			if err != nil {
				return err
			} else if got := string(rsp.Data); got != ab {
				return fmt.Errorf("got %q, want %q", got, ab)
			}
			return nil
		})

		// Send calls from B to A.
		ba := fmt.Sprintf("ba-call-%d", i+1)
		calls.Go(func() error {
			rsp, err := pb.Call(ctx, 100, []byte(ba))
			if err != nil {
				return err
			} else if got := string(rsp.Data); got != ba {
				return fmt.Errorf("got %q, want %q", got, ba)
			}
			return nil
		})
	}
	if err := calls.Wait(); err != nil {
		t.Errorf("Calls: %v", err)
	}
}

func rawChannel() (*io.PipeWriter, channel.IOChannel) {
	pr, tw := io.Pipe()
	_, pw := io.Pipe()
	return tw, channel.IO(pr, pw)
}

func mustErr(t *testing.T, err error, want string) {
	if err == nil {
		t.Fatalf("Got nil, want %v", want)
	} else if !strings.Contains(err.Error(), want) {
		t.Fatalf("Got %v, want %v", err, want)
	}
}

func slowEcho(_ context.Context, req *chirp.Request) (uint32, []byte, error) {
	time.Sleep(time.Duration(rand.Intn(100)+50) * time.Microsecond) // "work"
	return 0, req.Data, nil
}

// parseTestSpec parses a string giving test values to return from a method
// handler, and returns those values.
//
// Grammar:
//    ok:          -- return 0, nil, nil
//    ok: text     -- return 0, text, nil
//    ok: tag text -- return tag, text, nil
//    tag: tag     -- return tag, nil, nil
//    error: ...   -- return 0, nil, error(...)
//    peer?        -- return 0, x, nil where x == "present"/"absent"
//
// Any other value causes a panic.
func parseTestSpec(ctx context.Context, s string) (uint32, []byte, error) {
	ps := strings.Fields(s)
	switch ps[0] {
	case "ok:":
		if len(ps) == 1 {
			return 0, nil, nil
		} else if len(ps) == 2 {
			return 0, []byte(ps[1]), nil
		} else if len(ps) == 3 {
			n, err := strconv.Atoi(ps[1])
			if err == nil {
				return uint32(n), []byte(ps[2]), nil
			}
		}

	case "tag:":
		if len(ps) == 2 {
			n, err := strconv.Atoi(ps[1])
			if err == nil {
				return uint32(n), nil, nil
			}
		}

	case "peer?":
		if len(ps) == 1 {
			if chirp.ContextPeer(ctx) != nil {
				return 0, []byte("present"), nil
			}
			return 0, []byte("absent"), nil
		}

	case "error:":
		return 0, nil, errors.New(strings.Join(ps[1:], " "))
	}
	panic(fmt.Sprintf("Invalid test spec %q", s))
}

func mustResponse(t *testing.T, spec string) *chirp.Response {
	t.Helper()
	ps := strings.Fields(spec)
	if len(ps) < 2 { // code tag data ...
		t.Fatalf("Invalid response spec %q", spec)
	}
	code, err := strconv.Atoi(ps[0])
	if err != nil {
		t.Fatalf("invalid result code %q: %v", ps[0], err)
	}
	return &chirp.Response{
		Code: chirp.ResultCode(code),
		Data: []byte(strings.Join(ps[2:], " ")),
	}
}
