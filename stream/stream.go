// Package stream provides helpers for implementing streaming RPCs,
// where a single method call yields a stream of response payloads.
package stream

import (
	"context"
	"crypto/rand"
	"errors"
	"iter"
	"slices"

	"github.com/creachadair/chirp"
)

// A 24-byte random value acts as a capability when registered as a
// method. The value is not brute-forceable in reasonable time, and
// has negligible probability of collision. From the XChaCha20 RFC
// draft: 2^80 concurrently active capabilities on the same Peer has a
// 2^-32 probability of collision.
const capabilityLen = 24

// mkCapability returns a random capability.
func mkCapability() string {
	var ret [capabilityLen]byte
	rand.Read(ret[:])
	return string(ret[:])
}

// getCapability removes a capability from the end of req.Data and
// returns it.
func getCapability(req *chirp.Request) (string, error) {
	if len(req.Data) < capabilityLen {
		return "", errors.New("payload too short")
	}
	ret := string(req.Data[len(req.Data)-capabilityLen:])
	// Trim the slice capacity so that a cheeky peer can't just grow
	// the slice and recover the capability.
	req.Data = slices.Clip(req.Data[:len(req.Data)-capabilityLen])
	return ret, nil
}

// Call sends a call to the remote peer for the specified method and
// data, and yields a stream of responses. The response stream ends at
// the peer's discretion, or when ctx is canceled.
//
// The returned iterator yields zero or more (bs, nil) values. If the
// call ends unsuccessfully, the iterator ends the stream with a final
// (nil, err) tuple.
func Call(ctx context.Context, peer *chirp.Peer, method string, req []byte) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		capability := mkCapability()

		req = append(req, capability...)

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		// The peer streams values back to us by calling the passed-in
		// capability, which will run this handler in a different
		// goroutine. We're in an iterator func, we can't yield from a
		// random other goroutine. So, make a channel to smuggle
		// payloads from the capability handler back to us for
		// yielding.
		vals := make(chan []byte)
		peer.Handle(capability, func(callbackCtx context.Context, req *chirp.Request) ([]byte, error) {
			select {
			case vals <- req.Data:
				return nil, nil
			case <-ctx.Done():
				// Client side cancellation, we're already unwinding
				// this web of RPCs and just need the server to get on
				// with that.
				//
				// This is also important to unwedge a buggy server
				// that somehow manages to smuggle the capability out
				// of the streaming response lifecycle, and tries to
				// call us after the peer.Call() below has
				// returned. This path serves as "what are you doing
				// here? Go away" in the window between the call
				// returning and the handler registration being
				// cleaned up.
				return nil, ctx.Err()
			case <-callbackCtx.Done():
				// Server cancellation (or maybe indirectly client
				// cancellation too), we'll be told why when our Call
				// below returns.
				return nil, callbackCtx.Err()
			}
		})

		errch := make(chan error, 1)
		go func() {
			// Note, unregister the capability handler in this
			// goroutine, not the calling context, so that the other
			// side can't hit "method not found" as the iterator shuts
			// down.
			defer peer.Handle(capability, nil)
			defer close(errch)
			_, err := peer.Call(ctx, method, req)
			if ctx.Err() != nil {
				// This codepath isn't strictly necessary, but it
				// makes tests deterministic: a client-side
				// cancellation can manifest in two ways: the
				// peer.Call() notices first and returns ctx.Err()
				// locally; or a running stream callback sends a
				// cancellation error to the server, which then
				// bounces it back to us, and peer.Call returns that.
				//
				// This makes it tricky to reliably test client-side
				// cancellation, because the error is either a simple
				// local "context canceled", or it's a "context
				// canceled" wrapped in two layers of chirp.CallError,
				// with type information stripped off.
				//
				// This codepath prioritizes client cancellation over
				// whatever peer.Call returns, so that regardless of
				// how cancellation manifests, we reliably report it
				// as a local cancellation.
				//
				// Plus, aside from making unit tests deterministic,
				// it's less confusing to report a locally cancelled
				// context as a local error, rather than as "we called
				// the peer, it called something else, and that other
				// thing returned an error".
				errch <- ctx.Err()
			} else {
				errch <- err
			}
		}()

		for {
			select {
			case v := <-vals:
				if !yield(v, nil) {
					// Returning cancels the context that both the
					// peer call and callback run in, so they'll
					// gracefully unwind and clean themselves up. We
					// don't need to wait for that to happen.
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

// HandlerFunc is a variant of chirp.Handler that yields a stream of
// responses, rather than a single value. The returned iterator is
// expected to only yield a non-nil error as its final element,
// following zero or more error-free tuples.
type HandlerFunc func(context.Context, *chirp.Request) iter.Seq2[[]byte, error]

// Handle adapts a HandlerFunc into a chirp.Handler. The resulting
// handler must be invoked with [Call].
func Handle(peer *chirp.Peer, method string, fn HandlerFunc) {
	peer.Handle(method, func(ctx context.Context, req *chirp.Request) ([]byte, error) {
		capability, err := getCapability(req)
		if err != nil {
			return nil, err
		}

		peer := chirp.ContextPeer(ctx)

		for resp, err := range fn(ctx, req) {
			if err != nil {
				return nil, err
			}
			// We hand the context to the iterator and hope that it'll
			// yield to cancellation itself, but we can't force it
			// to. As a fallback, also explicitly bail on cancellation
			// here.
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if _, err = peer.Call(ctx, capability, resp); err != nil {
				return nil, err
			}
		}

		// We might have fallen out of the loop due to a cancellation,
		// if the iterator reacted to a cancellation by simply
		// returning, rather than yielding a final error. Rescue such
		// cases by returning any context error ourselves, if the
		// iterator didn't volunteer an error.
		return nil, ctx.Err()
	})
}
