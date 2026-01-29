// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

package chirp_test

import (
	"bytes"
	"context"
	"errors"
	"expvar"
	"fmt"
	"io"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/creachadair/chirp"
	"github.com/creachadair/chirp/channel"
	"github.com/creachadair/chirp/peers"
	"github.com/creachadair/mds/mnet"
	"github.com/creachadair/mds/mstr"
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
			if req.Method == "hidden" {
				return nil, chirp.ErrUnknownMethod
			}
			return []byte("wildcard"), nil
		}).
		Handle("1", func(ctx context.Context, req *chirp.Request) ([]byte, error) {
			return []byte("designated"), nil
		})

	call("", "wildcard", false)
	call("1", "designated", false)
	call("2", "wildcard", false)
	call("hidden", "?", true)

	// Unregister the wildcard handler and try again.
	loc.A.Handle("", nil)

	call("", "", true)
	call("1", "designated", false)
	call("2", "?", true)
	call("hidden", "?", true) // still fails
}

func TestCancellation(t *testing.T) {
	t.Run("Early", func(t *testing.T) {
		loc := peers.NewLocal()
		defer loc.Stop()

		loc.A.Handle("unseen", func(context.Context, *chirp.Request) ([]byte, error) {
			return nil, errors.New("you should not see this")
		})

		ctx, cancel := context.WithCancel(t.Context())
		cancel() // not deferred, we want it already finished at the time of the call

		rsp, err := loc.B.Call(ctx, "unseen", nil)
		if err == nil {
			t.Error("Call unexpectedly succeeded")
		} else if !errors.Is(err, context.Canceled) {
			t.Errorf("Call: got error = %+v, want %v", err, context.Canceled)
		}
		if rsp != nil {
			t.Errorf("Call reported response %+v, wanted none", rsp)
		}

		data, err := loc.A.Exec(ctx, "unseen", nil)
		if err == nil {
			t.Error("Exec unexpectedly succeeded")
		} else if !errors.Is(err, context.Canceled) {
			t.Errorf("Exec: got error = %+v, want %v", err, context.Canceled)
		}
		if len(data) != 0 {
			t.Errorf("Exec reported response %q, wanted none", data)
		}
	})

	t.Run("Late", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			loc := peers.NewLocal()
			defer loc.Stop()

			type packet struct {
				T chirp.PacketType
				P string
			}

			var wg sync.WaitGroup
			wg.Add(3) // there are three packets exchanged below

			var apkt []packet
			loc.A.LogPackets(func(pkt *chirp.Packet, dir chirp.PacketDir) {
				if dir == chirp.Recv {
					apkt = append(apkt, packet{T: pkt.Type, P: string(pkt.Payload)})
					wg.Done()
				}
			}).Handle("300", func(ctx context.Context, _ *chirp.Request) ([]byte, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			})

			var bpkt []packet
			loc.B.LogPackets(func(pkt *chirp.Packet, dir chirp.PacketDir) {
				if dir == chirp.Recv {
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
		})
	})
}

func TestPeerExec(t *testing.T) {
	defer leaktest.Check(t)()

	loc := peers.NewLocal()
	defer loc.Stop()

	loc.A.
		LogPackets(logPacket(t, "Peer A")).
		Handle("1", func(context.Context, *chirp.Request) ([]byte, error) {
			t.Log("handler: method 1 (makes no additional calls)")
			return []byte("ok"), nil
		}).
		Handle("2", func(ctx context.Context, req *chirp.Request) ([]byte, error) {
			t.Log("handler: method 2 (calls method 1)")
			rsp, err := chirp.ContextPeer(ctx).Call(ctx, "1", req.Data)
			if err != nil {
				return nil, err
			}
			return rsp.Data, nil
		}).
		Handle("3", func(ctx context.Context, req *chirp.Request) ([]byte, error) {
			t.Log("handler: method 3 (calls non-existent method 1000)")
			// Forward the request to method 1000 handler, should fail.
			// The data reported by this handler should not be seen by the caller.
			_, err := chirp.ContextPeer(ctx).Call(ctx, "1000", req.Data)
			return []byte("unseen"), err
		}).
		Handle("4", func(ctx context.Context, req *chirp.Request) ([]byte, error) {
			t.Log("handler: method 4 (execs method 2)")
			// Forward the request to method 2 handler, which should forward it to 1.
			return chirp.ContextPeer(ctx).Exec(ctx, "2", req.Data)
		})

	ctx := context.Background()
	t.Run("A/Exec2", func(t *testing.T) {
		// Verify that if we Exec on A, the Call from inside method 2 gets routed
		// to A rather than to B.
		rsp, err := loc.A.Exec(ctx, "2", nil)
		if err != nil {
			t.Fatalf("Exec 2: unexpected error: %v", err)
		}
		if got, want := string(rsp), "ok"; got != want {
			t.Errorf("Exec 2: got %q, want %q", got, want)
		}
	})
	t.Run("A/Exec3", func(t *testing.T) {
		// Verify that if we Exec on A, the UNKNOWN_METHOD error from its attempt
		// to call a (non-existent) method on B is reported as such.
		_, err := loc.A.Exec(ctx, "3", nil)
		if ce := (*chirp.CallError)(nil); !errors.As(err, &ce) {
			t.Fatalf("Exec 3: got error %[1]T (%[1]v), want CallError", err)
		} else if ce.Response.Code != chirp.CodeUnknownMethod {
			t.Errorf("Exec 3: got %v, want UNKNOWN_METHOD", ce)
		}
	})
	t.Run("B/Call4", func(t *testing.T) {
		// Verify that if we Call from B to A, then the Exec inside method 4 gets
		// routed to A itself rather than back to B.
		rsp, err := loc.B.Call(ctx, "4", nil)
		if err != nil {
			t.Errorf("Call 4: unexpected error: %v", err)
		} else if got, want := string(rsp.Data), "ok"; got != want {
			t.Errorf("Call 4: got %q, want %q", got, want)
		}
	})
	t.Run("HasContextPeer", func(t *testing.T) {
		// Verify that when we call a method from a top-level exec in the host,
		// the context passed to the resulting local handler has the peer
		// attached.
		loc.B.Handle("?", func(ctx context.Context, _ *chirp.Request) ([]byte, error) {
			return parseTestSpec(ctx, "peer?")
		})
		rsp, err := loc.B.Exec(context.Background(), "?", nil)
		if err != nil {
			t.Fatalf("Exec probe: unexpected error: %v", err)
		}
		if got := string(rsp); got != "present" {
			t.Errorf("Exec probe: got %q, want present", got)
		}
	})
}

func TestSlowCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		loc := peers.NewLocal()
		defer loc.Stop()

		logA := logPacket(t, "Peer A")

		var wg sync.WaitGroup
		wg.Add(2)
		var rspA []*chirp.Packet
		loc.A.
			Handle("666", func(context.Context, *chirp.Request) ([]byte, error) {
				time.Sleep(time.Second)
				return []byte("message in a bottle"), nil
			}).
			Handle("100", func(context.Context, *chirp.Request) ([]byte, error) {
				return []byte("ok"), nil
			}).
			LogPackets(func(pkt *chirp.Packet, dir chirp.PacketDir) {
				if dir == chirp.Send && pkt.Type == chirp.PacketResponse {
					rspA = append(rspA, pkt)
					wg.Done()
				}
				logA(pkt, dir)
			})

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

		wg.Wait()

		// Make sure we got the CANCELED response (eventually) from the peer.
		var found bool
		for i, pkt := range rspA {
			var rsp chirp.Response
			if err := rsp.Decode(pkt.Payload); err != nil {
				t.Fatalf("Decode packet %d: %v", i+1, err)
			}
			if rsp.RequestID == 1 && rsp.Code == chirp.CodeCanceled {
				found = true
			}
		}
		if !found {
			t.Error("No CANCELED response found for request 1")
		}
	})
}

func TestProtocolFatal(t *testing.T) {
	defer leaktest.Check(t)()

	t.Run("BadMagic", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			tw, ch := rawChannel()
			p := chirp.NewPeer().Start(ch)
			time.AfterFunc(time.Second, func() { p.Stop() })

			tw.Write([]byte{'*', '?', 0, 2, 0, 0, 0, 0})
			mustErr(t, p.Wait(), "invalid protocol magic")
		})
	})

	t.Run("ShortHeader", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			tw, ch := rawChannel()
			p := chirp.NewPeer().Start(ch)
			time.AfterFunc(time.Second, func() { p.Stop() })

			tw.Write([]byte{'\xc7', 0, 0, 2, 0, 0})
			tw.Close()
			mustErr(t, p.Wait(), "short packet header")
		})
	})

	t.Run("ShortPayload", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			tw, ch := rawChannel()
			p := chirp.NewPeer().Start(ch)
			time.AfterFunc(time.Second, func() { p.Stop() })

			tw.Write([]byte{'\xc7', 0, 0, 2, 0, 0, 0, 10, 'a', 'b', 'c', 'd'})
			tw.Close()
			mustErr(t, p.Wait(), "short payload")
		})
	})

	t.Run("BadRequest", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			tw, ch := rawChannel()
			p := chirp.NewPeer().Start(ch)
			time.AfterFunc(time.Second, func() { p.Stop() })

			tw.Write([]byte{'\xc7', 0, 0, 2, 0, 0, 0, 1, 'X'})
			mustErr(t, p.Wait(), "value truncated")
		})
	})

	t.Run("BadResponse", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			tw, ch := rawChannel()
			p := chirp.NewPeer().Start(ch)
			time.AfterFunc(time.Second, func() { p.Stop() })

			chirp.Packet{
				Type: chirp.PacketResponse,
				Payload: chirp.Response{
					RequestID: 100,
					Code:      100,
				}.Encode(),
			}.WriteTo(tw)
			mustErr(t, p.Wait(), "invalid result code")
		})
	})

	t.Run("CloseChannel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			done := make(chan error)
			stall := func(ctx context.Context, _ *chirp.Request) ([]byte, error) {
				defer close(done)
				<-ctx.Done()
				done <- ctx.Err()
				return nil, ctx.Err()
			}

			pr, tw := io.Pipe()
			tr, pw := io.Pipe()
			ch := channel.IO(pr, pw)
			p := chirp.NewPeer().Handle("22", stall).Start(ch)
			defer p.Stop()

			chirp.Packet{
				Type:    chirp.PacketRequest,
				Payload: chirp.Request{RequestID: 666, Method: "22"}.Encode(),
			}.WriteTo(tw)

			synctest.Wait()

			// Simulate the channel failing by closing the pipe.
			tw.Close() // time.AfterFunc(100*time.Millisecond, func() { tw.Close() })

			// Outbound calls MUST fail and report an error.
			var buf [64]byte
			nr, err := tr.Read(buf[:])
			if err != nil {
				t.Logf("Read response correctly failed: %v", err)
			} else {
				t.Errorf("Got response %#q, wanted error", string(buf[:nr]))
			}

			// Inbound calls MUST be cancelled and their results discarded.
			select {
			case err := <-done:
				t.Logf("Handler exited OK: %v", err)
			case <-time.After(time.Second):
				t.Error("Timed out waiting for handler to exit")
			}
			p.Stop()
		})
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
		LogPackets(func(pkt *chirp.Packet, dir chirp.PacketDir) {
			t.Logf("A: [%s] %v", dir, pkt)
			if dir == chirp.Recv {
				log = append(log, pkt)
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

	want := &chirp.Packet{
		Protocol: 99, // specifically, not 0
		Type:     chirp.PacketRequest,
		Payload: chirp.Request{
			RequestID: 12345,
			Method:    "foo",
			Data:      []byte("hello"),
		}.Encode(),
	}
	const encoded = "" +
		"\xc7\x63" + // magic + protocol(99)
		"\x00\x02" + // packet type: request
		"\x00\x00\x00\x0d" + // payload length (13)
		"\x00\x00\x30\x39" + // request ID 12345
		"\x03foo" + // method name
		"hello" // data
	var got bytes.Buffer
	if _, err := want.WriteTo(&got); err != nil {
		t.Fatalf("Write packet: %v", err)
	} else if got.String() != encoded {
		t.Errorf("Packet encoding:\ngot:  %q\nwant: %q", got.String(), encoded)
	}

	ac, bc := channel.Direct()
	a := chirp.NewPeer().LogPackets(func(pkt *chirp.Packet, dir chirp.PacketDir) {
		if dir == chirp.Send {
			// The peer should not send any packets.
			t.Errorf("Unexpected packet sent: %v", pkt)
		} else if diff := cmp.Diff(pkt, want); diff != "" {
			// The peer should get the packet we sent.
			t.Errorf("Received (-got, +want):\n%s", diff)
		} else {
			t.Logf("Got expected packet: %v", pkt)
		}
	}).Start(ac)
	defer func() { bc.Close(); a.Wait() }()

	// Send a request packet with an unrecognized protocol version.  The peer
	// should drop this packet, so we should not get a reply.
	if err := bc.Send(want); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
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

func TestDuplicate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		loc := peers.NewLocal()
		defer loc.Stop()

		loc.A.Handle("100", func(ctx context.Context, req *chirp.Request) ([]byte, error) {
			<-ctx.Done()
			t.Logf("Cancelled handler %+v", req)
			return nil, ctx.Err()
		}).LogPackets(logPacket(t, "Peer A"))

		var wg sync.WaitGroup
		wg.Add(2)

		var rspB []*chirp.Packet
		loc.B.LogPackets(func(pkt *chirp.Packet, dir chirp.PacketDir) {
			if dir == chirp.Recv {
				rspB = append(rspB, pkt)
				wg.Done()
			}
		})

		// Send a request with ID 12345 and wait for its handler to be running.
		loc.B.SendPacket(chirp.PacketRequest, chirp.Request{
			RequestID: 12345,
			Method:    "100",
		}.Encode())

		synctest.Wait()

		// Now send another request for ID 12345.
		loc.B.SendPacket(chirp.PacketRequest, chirp.Request{
			RequestID: 12345,
			Method:    "999",
		}.Encode())

		wg.Wait()

		// Verify that we got two DUPLICATE_REQUEST responses.
		wantResp := &chirp.Packet{
			Type:    chirp.PacketResponse,
			Payload: chirp.Response{RequestID: 12345, Code: chirp.CodeDuplicateID}.Encode(),
		}
		if diff := cmp.Diff(rspB, []*chirp.Packet{wantResp, wantResp}); diff != "" {
			t.Errorf("Wrong responses (-got, +want):\n%s", diff)
		}
	})
}

func runConcurrent(t *testing.T, pa, pb *chirp.Peer) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// To give the race detector something to push against, make the peers call
	// each other lots of times concurrently and wait for the responses.
	const numCalls = 128 // per peer

	calls := taskgroup.New(cancel)
	for i := range numCalls {

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
	return func(pkt *chirp.Packet, dir chirp.PacketDir) {
		t.Helper()
		t.Logf("%s: [%s] %v", tag, dir, pkt)
	}
}

func TestSplitAddress(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"", "unix"},
		{":", "unix"},

		{"12345", "tcp"},           // no colon, numeric
		{"nothing", "unix"},        // no colon, non-numeric
		{"like/a/file", "unix"},    // no colon, non-numeric
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

	t.Run("UnstartedPeer", func(t *testing.T) {
		ctx := context.Background()
		p := chirp.NewPeer().Handle("hi", func(context.Context, *chirp.Request) ([]byte, error) {
			panic("this should not be reached")
		})
		const message = "peer is not started"

		check := func(t *testing.T, val any) {
			if got, ok := val.(string); !ok {
				t.Errorf("Panic reported %q, want %q", val, message)
			} else if got != message {
				t.Errorf("Panic message: got %q, want %q", got, message)
			}
		}
		t.Run("Call", func(t *testing.T) {
			pval := mtest.MustPanicf(t, func() { p.Call(ctx, "hi", nil) },
				"call to unstarted peer should panic")
			check(t, pval)
		})
		t.Run("SendPacket", func(t *testing.T) {
			pval := mtest.MustPanicf(t, func() { p.SendPacket(100, []byte("hi")) },
				"send to unstarted peer should panic")
			check(t, pval)
		})
	})

	t.Run("EmptyError", func(t *testing.T) {
		if got := string(chirp.ErrorData{}.Encode()); got != "" {
			t.Errorf(`Encode empty error: got %q, want ""`, got)
		}
		if got := string(new(chirp.ErrorData).Encode()); got != "" {
			t.Errorf(`Encode empty error pointer: got %q, want ""`, got)
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

	var logCalls atomic.Int32
	checkLogs := func(want int) {
		if got := logCalls.Load(); got != int32(want) {
			t.Errorf("Log calls: got %d, want %d", got, want)
		}
	}
	loc.A.LogPackets(func(*chirp.Packet, chirp.PacketDir) { logCalls.Add(1) })

	cp := loc.A.Clone()
	cp.Handle("mirror", func(context.Context, *chirp.Request) ([]byte, error) { return []byte("y"), nil })

	x, y := channel.Direct()
	cp.Start(y)
	defer cp.Stop()
	cc := chirp.NewPeer().Start(x) // caller for cp
	defer cc.Stop()

	// Both A and its clone should respond to "test".
	// Make sure both share the same packet logger.
	checkLogs(0)
	checkCall(loc.B, "test", "x")
	checkLogs(2)
	checkCall(cc, "test", "x")
	checkLogs(4)

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

func TestHandlerPanic(t *testing.T) {
	defer leaktest.Check(t)()

	ctx := context.Background()
	loc := peers.NewLocal()
	defer loc.Stop()

	const failure = "the handler did the bad"
	loc.A.Handle("panic", func(context.Context, *chirp.Request) ([]byte, error) {
		panic(failure)
	})

	rsp, err := loc.B.Call(ctx, "panic", nil)
	if err == nil {
		t.Fatalf("Call panic: got %+v, want error", rsp)
	}
	var ce *chirp.CallError
	if !errors.As(err, &ce) {
		t.Errorf("Error %[1]T is not a CallError: %[1]v", err)
	} else if len(ce.Data) == 0 {
		t.Error("Error does not contain a call stack")
	} else {
		t.Logf("Got %d bytes of call stack (as desired):\n%s ...",
			len(ce.Data), mstr.Trunc(string(ce.Data), 550))
	}
	if !strings.Contains(err.Error(), failure) {
		t.Errorf("Error does not contain %q: %v", failure, err)
	}

}

func TestPeerMetrics(t *testing.T) {
	defer leaktest.Check(t)()

	ctx := context.Background()
	loc := peers.NewLocal()
	defer loc.Stop()

	loc.B.Handle("ok", func(context.Context, *chirp.Request) ([]byte, error) {
		return nil, nil
	})

	check := func(t *testing.T, p *chirp.Peer, metric string, want int64) int64 {
		t.Helper()
		v, ok := p.Metrics().Get(metric).(*expvar.Int)
		if !ok {
			t.Fatalf("Get metric %q: not found", metric)
		}
		got := v.Value()
		if want >= 0 && got != want {
			t.Errorf("Metric %q: got %d, want %d", metric, got, want)
		}
		return got
	}
	mustCall := func(t *testing.T, p *chirp.Peer, method string) {
		t.Helper()
		if _, err := p.Call(ctx, method, nil); err != nil {
			t.Fatalf("Call %q: unexpected error: %v", method, err)
		}
	}

	allMetrics := []string{
		"packets_received",
		"packets_sent",
		"packets_dropped",
		"calls_in",
		"calls_in_failed",
		"calls_active",
		"calls_out",
		"calls_out_failed",
		"cancels_in",
		"calls_pending",
	}
	checkZero := func(t *testing.T, p *chirp.Peer) {
		t.Helper()
		for _, name := range allMetrics {
			check(t, p, name, 0)
		}
	}

	// The peers have initially empty metrics.
	checkZero(t, loc.A)
	checkZero(t, loc.B)

	// After a call, the counters should be updated.
	t.Run("Call", func(t *testing.T) {
		mustCall(t, loc.A, "ok")
		check(t, loc.A, "calls_out", 1)
		check(t, loc.A, "calls_in", 0)
		check(t, loc.A, "packets_received", 1)
		check(t, loc.A, "packets_sent", 1)
		check(t, loc.B, "calls_in", 1)
		check(t, loc.B, "calls_out", 0)
		check(t, loc.B, "packets_received", 1)
		check(t, loc.B, "packets_sent", 1)
	})

	// Detached peers should have separate metrics.
	t.Run("Detach", func(t *testing.T) {
		ab, ba := channel.Direct()
		ca := loc.A.Clone().Detach().Start(ab)
		defer ca.Stop()
		cb := loc.B.Clone().Detach().Start(ba)
		defer cb.Stop()

		// Metrics are zero after detachment.
		checkZero(t, ca)
		checkZero(t, cb)
		mustCall(t, ca, "ok")
		check(t, loc.A, "calls_out", 1)
		check(t, loc.A, "calls_in", 0)
		check(t, loc.B, "calls_in", 1)
		check(t, loc.B, "calls_out", 0)
	})

	// The original peers are not affected by their detached cousins.
	t.Run("Original", func(t *testing.T) {
		mustCall(t, loc.A, "ok")
		check(t, loc.A, "calls_out", 2)
		check(t, loc.A, "calls_in", 0)
		check(t, loc.B, "calls_in", 2)
		check(t, loc.B, "calls_out", 0)
	})
}

func TestPeer_RemoteAddr(t *testing.T) {
	t.Run("Present", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			n := mnet.New(t.Name() + " network")
			defer n.Close()

			const clientAddr = "client.example.com:12345"
			const serverAddr = "server.example.com:6789"
			lst := n.MustListen("tcp", serverAddr)
			d := n.Dialer("tcp", clientAddr)

			p := chirp.NewPeer().Handle("check", func(ctx context.Context, req *chirp.Request) ([]byte, error) {
				addr := chirp.ContextPeer(ctx).RemoteAddr()
				t.Logf("Server got client address: %v", addr)
				if addr.String() != clientAddr {
					return nil, fmt.Errorf("got addr %q, want %q", addr.String(), clientAddr)
				}
				return []byte("ok"), nil
			})
			go func() {
				conn, err := lst.Accept()
				if err != nil {
					t.Errorf("Listen: %v", err)
					return
				}
				p.Start(channel.IO(conn, conn))
				p.Wait()
			}()

			conn, err := d.DialContext(t.Context(), "tcp", serverAddr)
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			t.Logf("Client got server address: %v", conn.RemoteAddr())

			q := chirp.NewPeer().Start(channel.IO(conn, conn))
			got, err := q.Call(t.Context(), "check", nil)
			if err != nil {
				t.Errorf("Call reported error: %v", err)
			} else {
				t.Logf("OK, call returned %v", got)
			}
			q.Stop()
		})
	})

	t.Run("Absent", func(t *testing.T) {
		loc := peers.NewLocal()
		defer loc.Stop()

		// Direct channels should not report a remote address.
		if addr := loc.A.RemoteAddr(); addr != nil {
			t.Errorf("Peer A: unexpected remote address: %v", addr)
		}
		if addr := loc.B.RemoteAddr(); addr != nil {
			t.Errorf("Peer B: unexpected remote address: %v", addr)
		}
	})

	t.Run("Unstarted", func(t *testing.T) {
		if addr := chirp.NewPeer().RemoteAddr(); addr != nil {
			t.Errorf("Unexpected remote address: %v", addr)
		}
	})
}
