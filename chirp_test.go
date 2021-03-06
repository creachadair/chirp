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
	loc.A.Handle(100, func(ctx context.Context, req *chirp.Request) ([]byte, error) {
		return parseTestSpec(ctx, string(req.Data))
	})

	tests := []struct {
		who      *chirp.Peer     // peer originating the call
		methodID uint32          // method ID to call
		input    string          // input for parseTestSpec (generates response)
		want     *chirp.Response // expected response
	}{
		{loc.B, 10, "n/a", &chirp.Response{Code: chirp.CodeUnknownMethod}},
		{loc.A, 20, "n/a", &chirp.Response{Code: chirp.CodeUnknownMethod}},
		{loc.A, 100, "n/a", &chirp.Response{Code: chirp.CodeUnknownMethod}},

		{loc.B, 100, "ok", &chirp.Response{}},                        // success, empty data
		{loc.B, 100, "ok yay", &chirp.Response{Data: []byte("yay")}}, // success, non-empty data

		{loc.B, 100, "error failure", &chirp.Response{
			Code: chirp.CodeServiceError,
			Data: chirp.ErrorData{Message: "failure"}.Encode(),
		}}, // service error, default handling
		{loc.B, 100, "edata 17 hey stuff", &chirp.Response{
			Code: chirp.CodeServiceError,
			Data: chirp.ErrorData{Code: 17, Message: "hey", Data: []byte("stuff")}.Encode(),
		}}, // service error, handler-provided code and data

		{loc.B, 100, "peer?", &chirp.Response{Data: []byte("present")}}, // check context peer
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
				t.Logf("CallError: %v", ce)
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
	loc.A.LogPacket(func(pkt *chirp.Packet) {
		apkt = append(apkt, packet{T: pkt.Type, P: string(pkt.Payload)})
		wg.Done()
	}).Handle(300, func(ctx context.Context, _ *chirp.Request) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	var bpkt []packet
	loc.B.LogPacket(func(pkt *chirp.Packet) {
		bpkt = append(bpkt, packet{T: pkt.Type, P: string(pkt.Payload)})
		wg.Done()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	rsp, err := loc.B.Call(ctx, 300, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Got %+v, %v; want %v", rsp, err, context.Canceled)
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
		p := chirp.NewPeer().Start(ch)
		time.AfterFunc(time.Second, func() { p.Stop() })

		tw.Write([]byte{'C', 'P', 1, 2, 0, 0, 0, 0})
		mustErr(t, p.Wait(), "invalid protocol version")
	})

	t.Run("ShortHeader", func(t *testing.T) {
		tw, ch := rawChannel()
		p := chirp.NewPeer().Start(ch)
		time.AfterFunc(time.Second, func() { p.Stop() })

		tw.Write([]byte{'C', 'P', 0, 2, 0, 0})
		tw.Close()
		mustErr(t, p.Wait(), "short packet header")
	})

	t.Run("ShortPayload", func(t *testing.T) {
		tw, ch := rawChannel()
		p := chirp.NewPeer().Start(ch)
		time.AfterFunc(time.Second, func() { p.Stop() })

		tw.Write([]byte{'C', 'P', 0, 2, 0, 0, 0, 10, 'a', 'b', 'c', 'd'})
		tw.Close()
		mustErr(t, p.Wait(), "short payload")
	})

	t.Run("BadRequest", func(t *testing.T) {
		tw, ch := rawChannel()
		p := chirp.NewPeer().Start(ch)
		time.AfterFunc(time.Second, func() { p.Stop() })

		tw.Write([]byte{'C', 'P', 0, 2, 0, 0, 0, 1, 'X'})
		mustErr(t, p.Wait(), "short request payload")
	})

	t.Run("BadResponse", func(t *testing.T) {
		tw, ch := rawChannel()
		p := chirp.NewPeer().Start(ch)
		time.AfterFunc(time.Second, func() { p.Stop() })

		tw.Write(chirp.Packet{
			Type: chirp.PacketResponse,
			Payload: chirp.Response{
				RequestID: 100,
				Code:      100,
			}.Encode(),
		}.Encode())
		mustErr(t, p.Wait(), "invalid result code")
	})

	t.Run("CloseChannel", func(t *testing.T) {
		ready := make(chan struct{})
		done := make(chan struct{})
		stall := func(ctx context.Context, _ *chirp.Request) ([]byte, error) {
			defer close(done)
			close(ready)
			<-ctx.Done()
			return nil, ctx.Err()
		}

		pr, tw := io.Pipe()
		tr, pw := io.Pipe()
		ch := channel.IO(pr, pw)
		p := chirp.NewPeer().Handle(22, stall).Start(ch)
		defer p.Stop()

		tw.Write(chirp.Packet{
			Type:    chirp.PacketRequest,
			Payload: chirp.Request{RequestID: 666, MethodID: 22}.Encode(),
		}.Encode())

		// Wait for the method handler to be running.
		<-ready

		// Simulate the channel failing by closing the pipe.
		time.AfterFunc(100*time.Millisecond, func() { tw.Close() })

		// Outbound calls MUST fail and report an error.
		var buf [64]byte
		nr, err := tr.Read(buf[:])
		if err != nil {
			t.Logf("Response correctly failed: %v", err)
		} else {
			t.Errorf("Got response %#q, wanted error", string(buf[:nr]))
		}

		// Inbound calls MUST be cancelled and their results discarded.
		select {
		case <-done:
			t.Log("Handler exited OK")
		case <-time.After(time.Second):
			t.Error("Timed out waiting for handler to exit")
		}
		p.Stop()
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
		LogPacket(func(pkt *chirp.Packet) {
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

func TestContextPlumbing(t *testing.T) {
	loc := peers.NewLocal()
	defer loc.Stop()

	type testKey struct{}
	loc.A.
		NewContext(func() context.Context {
			// Attach a known value to the base context.
			return context.WithValue(context.Background(), testKey{}, "ok")
		}).
		Handle(100, func(ctx context.Context, _ *chirp.Request) ([]byte, error) {
			// Verify that the base context is visible from ctx.
			v, ok := ctx.Value(testKey{}).(string)
			if !ok || v != "ok" {
				t.Error("Base context was not correctly plumbed")
			}
			return nil, nil
		})

	_, err := loc.B.Call(context.Background(), 100, nil)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
}

func TestCallback(t *testing.T) {
	loc := peers.NewLocal()
	defer loc.Stop()

	const numCallbacks = 5

	caller := func(ctx context.Context, req *chirp.Request) ([]byte, error) {
		peer := chirp.ContextPeer(ctx)

		v, err := strconv.Atoi(string(req.Data))
		if err != nil {
			return nil, err
		} else if v == numCallbacks {
			t.Logf("Peer %p complete (v=%d)", peer, numCallbacks)
			return []byte("ok"), nil
		}

		t.Logf("Peer %p callback v=%d", peer, v)
		rsp, err := peer.Call(ctx, req.MethodID, []byte(strconv.Itoa(v+1)))
		if err != nil {
			return nil, err
		}
		return rsp.Data, nil
	}

	// Each peer will ping-pong callbacks until the threshold has been reached,
	// then unwind returning the result from the furthest call all the way back
	// to the initial caller.
	loc.A.Handle(100, caller).LogPacket(logPacket(t, "Peer A"))
	loc.B.Handle(100, caller).LogPacket(logPacket(t, "Peer B"))

	rsp, err := loc.A.Call(context.Background(), 100, []byte("0"))
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	} else if got, want := string(rsp.Data), "ok"; got != want {
		t.Errorf("Call result: got %q, want %q", got, want)
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

func slowEcho(_ context.Context, req *chirp.Request) ([]byte, error) {
	time.Sleep(time.Duration(rand.Intn(100)+50) * time.Microsecond) // "work"
	return req.Data, nil
}

// parseTestSpec parses a string giving test values to return from a method
// handler, and returns those values.
//
// Grammar:
//    ok text...        -- return text, nil
//    error ...         -- return nil, error(...)
//    edata c msg data  -- return nil, ErrorData{c, msg, data}
//    peer?             -- return x, nil where x == "present"/"absent"
//
// Any other value causes a panic.
func parseTestSpec(ctx context.Context, s string) ([]byte, error) {
	ps := strings.Fields(s)
	switch ps[0] {
	case "ok":
		if len(ps) == 1 {
			return nil, nil
		}
		return []byte(strings.Join(ps[1:], " ")), nil

	case "error":
		return nil, errors.New(strings.Join(ps[1:], " "))

	case "edata":
		if len(ps) != 4 {
			break
		}
		c, err := strconv.ParseUint(ps[1], 10, 16)
		if err != nil {
			break
		}
		return nil, &chirp.ErrorData{
			Code:    uint16(c),
			Message: ps[2],
			Data:    []byte(ps[3]),
		}

	case "peer?":
		if len(ps) == 1 {
			if chirp.ContextPeer(ctx) != nil {
				return []byte("present"), nil
			}
			return []byte("absent"), nil
		}
	}
	panic(fmt.Sprintf("Invalid test spec %q", s))
}

func logPacket(t *testing.T, tag string) func(pkt *chirp.Packet) {
	return func(pkt *chirp.Packet) {
		t.Helper()
		t.Logf("%s: packet received: %v", tag, pkt)
	}
}
