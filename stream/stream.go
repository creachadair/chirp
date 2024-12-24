// Package chirpstream provides helpers for implementing streaming
// RPCs over Chirp, where a single method call yields a stream of
// response payloads.
package chirpstream

import (
	"context"
	"crypto/rand"
	"errors"
	"iter"

	"github.com/creachadair/chirp"
)

// Call sends a call to the remote peer for the specified method and
// data, and yields a stream of responses. The iterator returned by
// The returned iterator ends when ctx is canceled, or the peer ends
// the stream.
//
// The returned iterator yields zero or more (bs, nil) values. If the
// peer ends the stream with an error, the iterator yields a final
// (nil, err) tuple.
func CallStream(ctx context.Context, peer *chirp.Peer, method string, req []byte) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		// A 24-byte random value acts as a capability handle when
		// registered as a method: it's unguessable in reasonable time
		// (although on a chirp channel we're less worried about this
		// than in more open capability systems), and has negligible
		// probability of collision. From the XChaCha20 RFC draft, we
		// would have to generate 2^80 capability handles on the same
		// Peer for the probability of a collision to reach 2^-32.
		capability := make([]byte, 24)
		rand.Read(capability)

		req = append(req, capability...)

		// The peer streams values back to us by calling the passed-in
		// capability handle, which will run this handler in a
		// different goroutine. We're in an iterator func, we can't
		// yield from a random other goroutine. So, make a channel to
		// smuggle payloads from the capability handler back to us for
		// yielding.
		vals := make(chan []byte)
		peer.Handle(string(capability), func(callbackCtx context.Context, req *chirp.Request) ([]byte, error) {
			select {
			case vals <- req.Data:
				return nil, nil
			case <-ctx.Done():
				// Client side cancellation, we're already unwinding
				// this web of RPCs and just need the server to get on
				// with that.
				//
				// This is also important to unwedge a buggy server
				// that smuggles the capability handle out of the
				// streaming response lifecycle, and tries to call the
				// capability after peer.Call below has returned. This
				// path serves as "what are you doing here? Go away"
				// in the window between the call returning and the
				// RPC handler being cleaned up.
				return nil, ctx.Err()
			case <-callbackCtx.Done():
				// Server cancellation (or maybe indirectly client
				// cancellation too), we'll be told why when our Call
				// below returns.
				return nil, callbackCtx.Err()
			}
		})

		callCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		errch := make(chan error, 1)
		go func() {
			_, err := peer.Call(callCtx, method, req)
			errch <- err
			// Note, unregister the capability handler here, so that
			// the other side can't hit "method not found".
			defer peer.Handle(string(capability), nil)
		}()

		for {
			select {
			case v := <-vals:
				if !yield(v, nil) {
					// Initiate the cancellation and teardown of the
					// callback machinery. The braid of callback RPCs
					// can unwind itself graciously without us, no
					// need to wait.
					cancel()
					return
				}
			case err := <-errch:
				if err != nil {
					yield(nil, err)
				}
				return
			case <-ctx.Done():
				yield(nil, ctx.Err())
				return
			}
		}
	}
}

// StreamHandlerFn is a variant of chirp.Handler that yields a stream
// of responses, rather than a single value. The iterator returned by
// the handler is expected to only yield a non-nil error as its final
// element, following zero or more error-free tuples.
type StreamHandlerFn func(context.Context, *chirp.Request) iter.Seq2[[]byte, error]

// StreamHandler adapts a StreamHandlerFn into a chirp.Handler. The
// resulting handler must be called using CallStream, not a naked
// Peer.Call.
func StreamHandler(fn StreamHandlerFn) chirp.Handler {
	return func(ctx context.Context, req *chirp.Request) ([]byte, error) {
		if len(req.Data) < 24 {
			return nil, errors.New("bad request, payload missing capability handle")
		}

		peer := chirp.ContextPeer(ctx)

		capability := string(req.Data[len(req.Data)-24:])
		req.Data = req.Data[:len(req.Data)-24]

		for resp, err := range fn(ctx, req) {
			if err != nil {
				return nil, err
			}
			if _, err = peer.Call(ctx, capability, resp); err != nil {
				return nil, err
			}
		}

		return nil, nil
	}
}
