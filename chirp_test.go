// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

package chirp_test

import (
	"context"
	"errors"
	"expvar"
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
	"github.com/creachadair/mds/mtest"
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
		checkZero := func(m *expvar.Map, name string) {
			v := m.Get(name).(*expvar.Int).Value()
			if v != 0 {
				t.Errorf("Metric %q = %d, want 0", name, v)
			}
		}
		m := loc.A.Metrics()
		t.Logf("Metrics at exit: %v", m)

		// Check some basic properties of peer metrics.
		checkZero(m, "calls_active")
		checkZero(m, "calls_pending")
	}()

	// The test cases send a string in the requeset that is parsed by
	// parseTestSpec (see below) to control what the handler returns.
	loc.A.Handle("100", func(ctx context.Context, req *chirp.Request) ([]byte, error) {
		return parseTestSpec(ctx, string(req.Data))
	})

	tests := []struct {
		who        *chirp.Peer     // peer originating the call
		methodName string          // method name to call
		input      string          // input for parseTestSpec (generates response)
		want       *chirp.Response // expected response
	}{
		{loc.B, "10", "n/a", &chirp.Response{Code: chirp.CodeUnknownMethod}},
		{loc.A, "20", "n/a", &chirp.Response{Code: chirp.CodeUnknownMethod}},
		{loc.A, "100", "n/a", &chirp.Response{Code: chirp.CodeUnknownMethod}},

		{loc.B, "100", "ok", &chirp.Response{}},                        // success, empty data
		{loc.B, "100", "ok yay", &chirp.Response{Data: []byte("yay")}}, // success, non-empty data

		{loc.B, "100", "error failure", &chirp.Response{
			Code: chirp.CodeServiceError,
			Data: chirp.ErrorData{Message: "failure"}.Encode(),
		}}, // service error, default handling
		{loc.B, "100", "edata 17 hey stuff", &chirp.Response{
			Code: chirp.CodeServiceError,
			Data: chirp.ErrorData{Code: 17, Message: "hey", Data: []byte("stuff")}.Encode(),
		}}, // service error, handler-provided code and data (by value)
		{loc.B, "100", "*edata 101 goober nonsense", &chirp.Response{
			Code: chirp.CodeServiceError,
			Data: chirp.ErrorData{Code: 101, Message: "goober", Data: []byte("nonsense")}.Encode(),
		}}, // service error, handler-provided code and data (pointer)

		{loc.B, "100", "peer?", &chirp.Response{Data: []byte("present")}}, // check context peer
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("method-%s-%s", test.methodName, test.input), func(t *testing.T) {
			ctx := context.Background()

			rsp, err := test.who.Call(ctx, test.methodName, []byte(test.input))
			if err != nil {
				if rsp != nil {
					t.Errorf("Call: got response %+v with error %v", rsp, err)
				}
				ce, ok := err.(*chirp.CallError)
				if !ok {
					t.Fatalf("Call: got error %[1]T (%[1]v), want *CallError", err)
				}
				t.Logf("CallError: %v", ce)

				// If we got error data from the remote peer, verify that the
				// CallError correctly unpacked the data from the response.
				if ce.Err == nil {
					var ed chirp.ErrorData
					if err := ed.Decode(ce.Response.Data); err != nil {
						t.Errorf("Decode response ErrorData: %v", err)
					} else if diff := cmp.Diff(ed, ce.ErrorData); diff != "" {
						t.Errorf("ErrorData (-got, +want):\n%s", diff)
					}
					t.Logf("Response ErrorData: %v", ed)
				}
				rsp = ce.Response
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

func TestMethodLen(t *testing.T) {
	defer leaktest.Check(t)()

	loc := peers.NewLocal()
	defer loc.Stop()

	tooLongName := strings.Repeat("m", chirp.MaxMethodLen+5)

	t.Run("HandleTooLong", func(t *testing.T) {
		got := mtest.MustPanic(t, func() { loc.A.Handle(tooLongName, nil) }).(string)
		if !strings.Contains(got, "name too long") {
			t.Errorf("Handle: got %q, want too long", got)
		}
	})

	t.Run("CallTooLong", func(t *testing.T) {
		var cerr *chirp.CallError
		rsp, err := loc.A.Call(context.Background(), tooLongName, nil)
		if rsp != nil {
			t.Errorf("Call: unexpected response: %v", err)
		}
		if !errors.As(err, &cerr) {
			t.Errorf("Call: unexpected error: got %v, want CallError", err)
		} else if got := cerr.Err.Error(); !strings.Contains(got, "name too long") {
			t.Errorf("Call: got %q, want too long", got)
		}
	})
}

func TestWildcard(t *testing.T) {
	defer leaktest.Check(t)()

	loc := peers.NewLocal()
	defer loc.Stop()

	ctx := context.Background()
	call := func(mid, want string, fail bool) {
		t.Helper()

		rsp, err := loc.B.Call(ctx, mid, nil)
		if err != nil {
			if fail {
				t.Logf("Call %q: got err=%v [OK]", mid, err)
			} else {
				t.Errorf("Call %q: unexpected error: %v", mid, err)
			}
			return
		} else if fail {
			t.Errorf("Call %q: should have failed", mid)
		}
		if got := string(rsp.Data); got != want {
			t.Errorf("Call %q: got %q, want %q", mid, got, want)
		}
	}

	loc.A.
		Handle("", func(ctx context.Context, req *chirp.Request) ([]byte, error) {
			return []byte("wildcard"), nil
		}).
		Handle("1", func(ctx context.Context, req *chirp.Request) ([]byte, error) {
			return []byte("designated"), nil
		})

	call("", "wildcard", false)
	call("1", "designated", false)
	call("2", "wildcard", false)

	// Unregister the wildcard handler and try again.
	loc.A.Handle("", nil)

	call("", "", true)
	call("1", "designated", false)
	call("2", "?", true)
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
	loc.A.LogPackets(func(pkt chirp.PacketInfo) {
		if !pkt.Sent {
			apkt = append(apkt, packet{T: pkt.Type, P: string(pkt.Payload)})
			wg.Done()
		}
	}).Handle("300", func(ctx context.Context, _ *chirp.Request) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	var bpkt []packet
	loc.B.LogPackets(func(pkt chirp.PacketInfo) {
		if !pkt.Sent {
			bpkt = append(bpkt, packet{T: pkt.Type, P: string(pkt.Payload)})
			wg.Done()
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	rsp, err := loc.B.Call(ctx, "300", nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Got %+v, %v; want %v", rsp, err, context.Canceled)
	}

	wg.Wait()

	// B should have sent a Request followed by a Cancellation.
	if diff := cmp.Diff([]packet{
		// Request(1, 300, nil)
		{T: chirp.PacketRequest, P: "\x00\x00\x00\x01\x03300"},
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

func TestPeerExec(t *testing.T) {
	defer leaktest.Check(t)()

	loc := peers.NewLocal()
	defer loc.Stop()

	loc.A.
		LogPackets(logPacket(t, "Peer A")).
		Handle("1", func(context.Context, *chirp.Request) ([]byte, error) {
			t.Log("handler: method 1")
			return []byte("ok"), nil
		}).
		Handle("2", func(ctx context.Context, req *chirp.Request) ([]byte, error) {
			t.Log("handler: method 2")
			// Forward the request to method 1 handler, should succeed.
			return chirp.ContextPeer(ctx).Exec(ctx, "1", req.Data)
		}).
		Handle("3", func(ctx context.Context, req *chirp.Request) ([]byte, error) {
			t.Log("handler: method 3")
			// Forward the request to method 1000 handler, should fail.
			// The data reported by this handler should not be seen by the caller.
			_, err := chirp.ContextPeer(ctx).Exec(ctx, "1000", req.Data)
			return []byte("unseen"), err
		}).
		Handle("4", func(ctx context.Context, req *chirp.Request) ([]byte, error) {
			t.Log("handler: method 4")
			// Forward the request to method 2 handler, which should forward it to 1.
			return chirp.ContextPeer(ctx).Exec(ctx, "2", req.Data)
		})

	ctx := context.Background()
	for _, mid := range []string{"2", "4"} {
		t.Run(fmt.Sprintf("Call%s", mid), func(t *testing.T) {
			rsp, err := loc.B.Call(ctx, mid, nil)
			if err != nil {
				t.Errorf("Call %q: unexpected error: %v", mid, err)
			}
			if got, want := string(rsp.Data), "ok"; got != want {
				t.Errorf("Call %q: got %q, want %q", mid, got, want)
			}
		})
	}
	t.Run("Call3", func(t *testing.T) {
		rsp, err := loc.B.Call(ctx, "3", nil)
		var cerr *chirp.CallError
		if !errors.As(err, &cerr) {
			t.Errorf("Call 3: got (%v, %v), want CallError", rsp, err)
		} else if got := cerr.Response.Code; got != chirp.CodeUnknownMethod {
			t.Errorf("Call 3: response code is %v, want %v", got, chirp.CodeUnknownMethod)
		}
		if rsp != nil {
			t.Errorf("Call 3: response is %v, want nil", rsp)
		}
	})
}

func TestSlowCancellation(t *testing.T) {
	defer leaktest.Check(t)()

	loc := peers.NewLocal()
	defer loc.Stop()

	stop := make(chan struct{})     // close to release the blocked 666 handler
	returned := make(chan struct{}) // closed when the 666 handler returns
	loc.A.
		Handle("666", func(context.Context, *chirp.Request) ([]byte, error) {
			defer close(returned)
			<-stop // block until released
			return []byte("message in a bottle"), nil
		}).
		Handle("100", func(context.Context, *chirp.Request) ([]byte, error) {
			return []byte("ok"), nil
		}).
		LogPackets(logPacket(t, "Peer A"))

	done := make(chan struct{}) // closed when Call(666) returns
	go func() {
		defer close(stop)
		select {
		case <-done:
			// OK, we got past the call
		case <-time.After(5 * time.Second):
			t.Error("Timeout waiting for Call to return")
		}
	}()

	// Verify that a call times out and returns control to the calling peer even
	// if the remote peer has not acknowledged the cancellation yet.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if rsp, err := loc.B.Call(ctx, "666", nil); err == nil {
		t.Errorf("Call: unexpectedly succeeded: %v", rsp)
	} else {
		t.Logf("Call correctly failed: %v", err)
	}

	// Verify that the peer did not yield the unresolved request ID, which would
	// otherwise be reused.
	if rsp, err := loc.B.Call(context.Background(), "100", nil); err != nil {
		t.Errorf("Call 100 unexpectedly failed: %v", err)
	} else if got, want := string(rsp.Data), "ok"; got != want {
		t.Errorf("Call 100: got %q, want %q", got, want)
	}

	close(done) // also releases the blocked 666 handler
	<-returned

	// TODO(creachadair): Verify that A sent a CANCELED packet for ID 1.
}

func TestProtocolFatal(t *testing.T) {
	defer leaktest.Check(t)()

	t.Run("BadMagic", func(t *testing.T) {
		tw, ch := rawChannel()
		p := chirp.NewPeer().Start(ch)
		time.AfterFunc(time.Second, func() { p.Stop() })

		tw.Write([]byte{'C', 'X', 0, 2, 0, 0, 0, 0})
		mustErr(t, p.Wait(), "invalid protocol magic")
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
		p := chirp.NewPeer().Handle("22", stall).Start(ch)
		defer p.Stop()

		tw.Write(chirp.Packet{
			Type:    chirp.PacketRequest,
			Payload: chirp.Request{RequestID: 666, Method: "22"}.Encode(),
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
	defer leaktest.Check(t)()

	loc := peers.NewLocal()
	defer loc.Stop()

	var log []*chirp.Packet
	var got []*chirp.Packet
	var wg sync.WaitGroup
	wg.Add(2)
	loc.A.
		HandlePacket(128, func(ctx context.Context, pkt *chirp.Packet) error {
			defer wg.Done()
			got = append(got, pkt)

			// Send a "reply" packet back to the caller. This does not need to be
			// the same packet type that we received.
			rsp := string(pkt.Payload) + " reply"
			return chirp.ContextPeer(ctx).SendPacket(129, []byte(rsp))
		}).
		LogPackets(func(pkt chirp.PacketInfo) {
			if !pkt.Sent {
				log = append(log, pkt.Packet)
			}
		})
	loc.B.
		HandlePacket(129, func(ctx context.Context, pkt *chirp.Packet) error {
			defer wg.Done()
			log = append(log, pkt)
			return nil
		})

	// Unknown packet type: Logged but discarded.
	p1 := &chirp.Packet{Type: 100, Payload: []byte("unrecognized")}

	// Registered custom packet type: Logged and "processed".
	p2 := &chirp.Packet{Type: 128, Payload: []byte("custom")}

	// A packet handler can also send packets back to its caller.
	p3 := &chirp.Packet{Type: 129, Payload: []byte("custom reply")}

	if err := loc.B.SendPacket(p1.Type, p1.Payload); err != nil {
		t.Fatalf("SendPacket: %v", err)
	}
	if err := loc.B.SendPacket(p2.Type, p2.Payload); err != nil {
		t.Fatalf("SendPacket: %v", err)
	}

	// Stop the peer so the callbacks settle.
	wg.Wait()
	if err := loc.Stop(); err != nil {
		t.Errorf("Stop peer: %v", err)
	}

	if diff := cmp.Diff([]*chirp.Packet{p1, p2, p3}, log); diff != "" {
		t.Errorf("Packet log (-want, +got):\n%s", diff)
	}
	if diff := cmp.Diff([]*chirp.Packet{p2}, got); diff != "" {
		t.Errorf("Custom packet (-want, +got):\n%s", diff)
	}
}

func TestProtocolVersion(t *testing.T) {
	defer leaktest.Check(t)()

	pkt := &chirp.Packet{
		Protocol: 99, // specifically, not 0
		Type:     chirp.PacketRequest,
		Payload: chirp.Request{
			RequestID: 12345,
			Method:    "foo",
			Data:      []byte("hello"),
		}.Encode(),
	}

	ac, bc := channel.Direct()
	a := chirp.NewPeer().LogPackets(func(pi chirp.PacketInfo) {
		if pi.Sent {
			// The peer should not send any packets.
			t.Errorf("Unexpected packet sent: %v", pi)
		} else if diff := cmp.Diff(pi.Packet, pkt); diff != "" {
			// The peer should get the packet we sent.
			t.Errorf("Received (-got, +want):\n%s", diff)
		} else {
			t.Logf("Got expected packet: %v", pi)
		}
	}).Start(ac)
	defer func() { bc.Close(); a.Wait() }()

	// Send a request packet with an unrecognized protocol version.  The peer
	// should drop this packet, so we should not get a reply.
	if err := bc.Send(pkt); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
}

func TestOnExit(t *testing.T) {
	t.Run("CloseChannel", func(t *testing.T) {
		defer leaktest.Check(t)()

		loc := peers.NewLocal()
		defer loc.B.Wait()

		var cbCalled bool
		loc.A.OnExit(func(err error) {
			cbCalled = true
			if err != nil {
				t.Errorf("OnExit got an unexpected error: %v", err)
			}
		})

		time.AfterFunc(5*time.Millisecond, func() { loc.A.Stop() })

		if err := loc.A.Wait(); err != nil {
			t.Errorf("Wait: got %v, want nil", err)
		}
		if !cbCalled {
			t.Error("OnExit was not called")
		}
	})

	t.Run("BadPacket", func(t *testing.T) {
		defer leaktest.Check(t)()

		sr, cw := io.Pipe()
		_, sw := io.Pipe()
		srv := channel.IO(sr, sw)

		var cbCalled bool
		var cbErr error
		p := chirp.NewPeer().Start(srv).OnExit(func(err error) {
			cbCalled = true
			cbErr = err
		})

		cw.Write([]byte("CP\x00\x01\x00\x00\x00")) // short packet header
		cw.Close()

		if err := p.Wait(); err == nil {
			t.Error("Wait should have reported an error")
		} else {
			t.Logf("Wait reported: %v (OK)", err)
		}

		if !cbCalled {
			t.Error("OnExit was not called")
		} else if cbErr == nil {
			t.Error("OnExit should have reported an error")
		} else {
			t.Logf("OnExit reported: %v (OK)", cbErr)
		}
	})
}

func TestContextPlumbing(t *testing.T) {
	defer leaktest.Check(t)()

	loc := peers.NewLocal()
	defer loc.Stop()

	type testKey struct{}
	loc.A.
		NewContext(func() context.Context {
			// Attach a known value to the base context.
			return context.WithValue(context.Background(), testKey{}, "ok")
		}).
		Handle("100", func(ctx context.Context, _ *chirp.Request) ([]byte, error) {
			// Verify that the base context is visible from ctx.
			v, ok := ctx.Value(testKey{}).(string)
			if !ok || v != "ok" {
				t.Error("Base context was not correctly plumbed")
			}
			return nil, nil
		})

	_, err := loc.B.Call(context.Background(), "100", nil)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
}

func TestCallback(t *testing.T) {
	defer leaktest.Check(t)()

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
		rsp, err := peer.Call(ctx, req.Method, []byte(strconv.Itoa(v+1)))
		if err != nil {
			return nil, err
		}
		return rsp.Data, nil
	}

	// Each peer will ping-pong callbacks until the threshold has been reached,
	// then unwind returning the result from the furthest call all the way back
	// to the initial caller.
	loc.A.Handle("100", caller).LogPackets(logPacket(t, "Peer A"))
	loc.B.Handle("100", caller).LogPackets(logPacket(t, "Peer B"))

	rsp, err := loc.A.Call(context.Background(), "100", []byte("0"))
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	} else if got, want := string(rsp.Data), "ok"; got != want {
		t.Errorf("Call result: got %q, want %q", got, want)
	}
}

func TestConcurrency(t *testing.T) {
	defer leaktest.Check(t)()

	t.Run("Local", func(t *testing.T) {
		defer leaktest.Check(t)()

		loc := peers.NewLocal()
		defer loc.Stop()

		loc.A.Handle("100", slowEcho)
		loc.B.Handle("200", slowEcho)

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

		pa.Handle("100", slowEcho)
		pb.Handle("200", slowEcho)

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
			rsp, err := pa.Call(ctx, "200", []byte(ab))
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
			rsp, err := pb.Call(ctx, "100", []byte(ba))
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
//
//	ok text...        -- return text, nil
//	error ...         -- return nil, error(...)
//	edata c msg data  -- return nil, ErrorData{c, msg, data}
//	*edata c msg data -- return nil, &ErrorData{c, msg, data}
//	peer?             -- return x, nil where x == "present"/"absent"
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

	case "edata", "*edata":
		if len(ps) != 4 {
			break
		}
		c, err := strconv.ParseUint(ps[1], 10, 16)
		if err != nil {
			break
		}
		ed := chirp.ErrorData{
			Code:    uint16(c),
			Message: ps[2],
			Data:    []byte(ps[3]),
		}
		if ps[0] == "*edata" {
			return nil, &ed
		}
		return nil, ed

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

func logPacket(t *testing.T, tag string) chirp.PacketLogger {
	return func(pkt chirp.PacketInfo) {
		t.Helper()
		t.Logf("%s: %v", tag, pkt)
	}
}

func TestSplitAddress(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"", "unix"},
		{":", "unix"},

		{"nothing", "unix"},        // no colon
		{"like/a/file", "unix"},    // no colon
		{"no-port:", "unix"},       // empty port
		{"file/with:port", "unix"}, // slashes in host
		{"path/with:404", "unix"},  // slashes in host
		{"mangled:@3", "unix"},     // non-alphanumerics in port
		{"[::1]:2323", "tcp"},      // bracketed IPv6 with port

		{":80", "tcp"},            // numeric port
		{":dumb-crud", "tcp"},     // service name
		{"localhost:80", "tcp"},   // host and numeric port
		{"localhost:http", "tcp"}, // host and service name
	}
	for _, test := range tests {
		got, addr := chirp.SplitAddress(test.input)
		if got != test.want {
			t.Errorf("SplitAddress(%q) type: got %q, want %q", test.input, got, test.want)
		}
		if addr != test.input {
			t.Errorf("SplitAddress(%q) addr: got %q, want %q", test.input, addr, test.input)
		}
	}
}

func TestRegression(t *testing.T) {
	t.Run("ErrorDataSize", func(t *testing.T) {
		const input = "\x00\x01\x00\x04abc"

		var ed chirp.ErrorData
		if err := ed.Decode([]byte(input)); err == nil {
			t.Errorf("ErrorData: got %#v, wanted error", ed)
		} else {
			t.Logf("Decoding ErrorData: got expected error: %v", err)
		}
	})

	t.Run("ErrorUTF8", func(t *testing.T) {
		const input = "\x01\x02\x00\x04abc\xc0----"

		var ed chirp.ErrorData
		if err := ed.Decode([]byte(input)); err == nil {
			t.Errorf("ErrorData: got %#v, wanted error", ed)
		} else {
			t.Logf("Decoding ErrorData: got expected error: %v", err)
		}
	})
}

func TestClone(t *testing.T) {
	loc := peers.NewLocal()
	defer loc.Stop()

	type echoKey struct{}
	ctx := context.Background()
	vctx := context.WithValue(context.Background(), echoKey{}, "x")
	checkCall := func(p *chirp.Peer, method, want string) {
		t.Helper()
		if rsp, err := p.Call(ctx, method, nil); err != nil {
			t.Errorf("Call %q: unexpected error: %v", method, err)
		} else if got := string(rsp.Data); got != want {
			t.Errorf("Call %q: got %q, want %q", method, got, want)
		}
	}

	const ptype = 129
	var acount, ccount int
	var pg sync.WaitGroup

	checkSend := func(p *chirp.Peer) {
		t.Helper()
		pg.Add(1)
		if err := p.SendPacket(ptype, nil); err != nil {
			t.Fatalf("SendPacket unexpectedly failed: %v", err)
		}
	}

	loc.A.NewContext(func() context.Context { return vctx })
	loc.A.Handle("test", func(ctx context.Context, _ *chirp.Request) ([]byte, error) {
		return []byte(ctx.Value(echoKey{}).(string)), nil
	})
	loc.A.HandlePacket(ptype, func(context.Context, *chirp.Packet) error { defer pg.Done(); acount++; return nil })

	cp := loc.A.Clone()
	cp.Handle("mirror", func(context.Context, *chirp.Request) ([]byte, error) { return []byte("y"), nil })

	x, y := channel.Direct()
	cp.Start(y)
	defer cp.Stop()
	cc := chirp.NewPeer().Start(x) // caller for cp
	defer cc.Stop()

	// Both A and its clone should respond to "test".
	checkCall(loc.B, "test", "x")
	checkCall(cc, "test", "x")

	// The clone has a handler for "mirror" but A does not.
	checkCall(cc, "mirror", "y")
	if rsp, err := loc.B.Call(ctx, "mirror", nil); err == nil {
		t.Errorf("Call mirror: got %v, want error", rsp)
	}

	// Both A and its clone should share a packet handler for ptype.
	// Note the waitgroup dance is to ensure we sync with the handler.
	checkSend(loc.B)
	pg.Wait() // so they don't race on writing acount
	checkSend(cc)
	pg.Wait()
	if acount != 2 {
		t.Errorf("After send: got %d packets, want %d", acount, 2)
	}

	// Now if we modify the packet handler, they should diverge.
	cp.HandlePacket(ptype, func(context.Context, *chirp.Packet) error { defer pg.Done(); ccount++; return nil })
	checkSend(loc.B) // goes to original handler
	checkSend(cc)    // goes to updated handler
	pg.Wait()
	if acount != 3 || ccount != 1 {
		t.Errorf("After send: got %d, %d; want %d, %d", acount, ccount, 3, 1)
	}
}
