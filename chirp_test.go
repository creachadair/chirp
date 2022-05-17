package chirp_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/creachadair/chirp"
	"github.com/creachadair/chirp/channel"
	"github.com/creachadair/chirp/peers"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestPeer(t *testing.T) {
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

func TestCallTimeout(t *testing.T) {
	loc := peers.NewLocal()
	defer loc.Stop()

	// Add a method handler that blocks until cancelled, then closes done.
	// This affirms that cancellation is propagated to the remote peer.
	done := make(chan bool, 1)
	loc.A.Handle(300, func(ctx context.Context, _ *chirp.Request) (uint32, []byte, error) {
		defer close(done)
		<-ctx.Done()
		done <- true
		return 0, nil, ctx.Err()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	rsp, err := loc.B.Call(ctx, 300, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Got %+v, %v; want %v", rsp, err, context.DeadlineExceeded)
	}
	select {
	case <-done:
		t.Log("OK: Method handler was cancelled")
	case <-time.After(5 * time.Second):
		t.Fatal("Method handler was not cancelled in a timely fashion")
	}
}

func TestProtocolFatal(t *testing.T) {
	t.Run("BadMagic", func(t *testing.T) {
		tw, ch := rawChannel()
		p := new(chirp.Peer).Start(ch)
		time.AfterFunc(time.Second, func() { p.Stop() })

		tw.Write([]byte{'C', 'P', 1, 2, 0, 0, 0, 0})
		mustErr(t, p.Wait(), "invalid protocol version")
	})

	t.Run("EOF", func(t *testing.T) {
		tw, ch := rawChannel()
		p := new(chirp.Peer).Start(ch)
		time.AfterFunc(time.Second, func() { p.Stop() })

		tw.Close()
		mustErr(t, p.Wait(), "short packet header")
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
	tag, err := strconv.Atoi(ps[1])
	if err != nil {
		t.Fatalf("Invalid tag %q: %v", ps[1], err)
	}
	return &chirp.Response{
		Code: chirp.ResultCode(code),
		Tag:  uint32(tag),
		Data: []byte(strings.Join(ps[2:], " ")),
	}
}