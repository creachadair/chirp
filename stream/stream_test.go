package stream_test

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"strings"
	"testing"

	"github.com/creachadair/chirp"
	"github.com/creachadair/chirp/peers"
	"github.com/creachadair/chirp/stream"
)

func TestStream(t *testing.T) {
	tests := []struct {
		in      string
		want    []string
		wantErr string
	}{
		{"stream foo bar", vals("foo", "bar"), ""},
		{"stream foo bar, err", vals("foo", "bar"), "service error: test"},
		{"err", vals(), "service error: test"},
		{"req, req, stream foo", vals("req", "req", "foo"), ""},
		// server-side cancellation just before successful stream end
		{"stream foo, server-cancel", vals("foo"), "service error: context canceled"},
		// server-side cancellation that HandlerFunc ignores
		{"stream foo, server-cancel, stream bar qux", vals("foo"), "service error: context canceled"},
		// server-side cancellation that HandlerFunc obeys
		{"stream foo, server-cancel, return-canceled", vals("foo"), "service error: context canceled"},
		// client-side cancellation
		{"stream foo, client-cancel, stream bar qux", vals("foo"), "context canceled"},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			ps := peers.NewLocal()

			ctx, clientCancel := context.WithCancel(context.Background())
			defer clientCancel()

			stream.Handle(ps.B, "stream", parseStreamSpec(t, tc.in))
			ps.B.NewContext(func() context.Context {
				// Give the server-side handler access to both client-side
				// and server-side CancelFuncs, so that parseStreamSpec
				// can drive cancellation on either end of the Peer. To
				// synchronize client-side cancellation properly, also
				// smuggle in the client-side context, which will be used
				// exclusively for synchronizing cancellation.
				serverCtx, serverCancel := context.WithCancel(context.Background())
				serverCtx = context.WithValue(serverCtx, serverCancelContextKey{}, serverCancel)
				serverCtx = context.WithValue(serverCtx, clientCtxContextKey{}, ctx)
				serverCtx = context.WithValue(serverCtx, clientCancelContextKey{}, clientCancel)
				return serverCtx
			})

			var got []string
			var gotErr error
			for resp, err := range stream.Call(ctx, ps.A, "stream", []byte("req")) {
				if err != nil {
					gotErr = err
					break
				}
				got = append(got, string(resp))
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got stream %v, want %v", got, tc.want)
			}
			if gotErr != nil {
				// Errors transit over a network, so we can't compare with
				// errors.Is. Compare strings instead as an approximation.
				if gotErr.Error() != tc.wantErr {
					t.Fatalf("unexpected error %q, want %q", gotErr, tc.wantErr)
				}
			} else if tc.wantErr != "" {
				t.Fatalf("stream didn't yield error, want %q", tc.wantErr)
			}
		})
	}
}

func parseStreamSpec(t *testing.T, s string) stream.HandlerFunc {
	return func(ctx context.Context, req *chirp.Request) iter.Seq2[[]byte, error] {
		return func(yield func([]byte, error) bool) {
			for _, cmd := range strings.Split(s, ",") {
				fs := strings.Fields(cmd)
				switch fs[0] {
				case "stream":
					for _, v := range fs[1:] {
						if !yield([]byte(v), nil) {
							return
						}
					}
				case "req":
					if !yield(req.Data, nil) {
						return
					}
				case "err":
					yield(nil, testErr)
					return
				case "server-cancel":
					cancel := ctx.Value(serverCancelContextKey{}).(context.CancelFunc)
					cancel()
					// Closing of ctx.Done can happen asynchronously
					// after cancel returns. Wait on ctx.Done
					// ourselves, to ensure that the caller will
					// reliably see a canceled context as well.
					<-ctx.Done()
				case "client-cancel":
					cancel := ctx.Value(clientCancelContextKey{}).(context.CancelFunc)
					cancel()
					// Same as above, force synchronization of the
					// client's context cancellation.
					clientCtx := ctx.Value(clientCtxContextKey{}).(context.Context)
					<-clientCtx.Done()
				case "return-canceled":
					if ctx.Err() == nil {
						t.Errorf("parseStreamSpec instructed to return-canceled, but ctx isn't canceled")
					}
					yield(nil, ctx.Err())
					return
				default:
					t.Errorf("unknown parseStreamSpec command %q", fs[0])
				}
			}
		}
	}
}

type clientCancelContextKey struct{}
type clientCtxContextKey struct{}
type serverCancelContextKey struct{}

var testErr = errors.New("test")

func vals(vs ...string) []string {
	return vs
}
