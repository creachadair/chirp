// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

package chirp

import (
	"context"
	"errors"
	"expvar"
	"fmt"
	"io"
	"maps"
	"net"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creachadair/taskgroup"
)

// A Channel is a reliable stream of packets shared by two peers.
//
// The methods of an implementation must be safe for concurrent use by one
// sender and one receiver.
type Channel interface {
	// Send the packet in binary format to the receiver.
	Send(Packet) error

	// Receive the next available packet from the channel.
	Recv() (Packet, error)

	// Close the channel, causing any pending send or receive operations to
	// terminate and report an error. After a channel is closed, all further
	// operations on it must report an error.
	Close() error
}

// A Handler processes a request from the remote peer.  A handler can obtain
// the peer from its context argument using the [ContextPeer] helper.
//
// By default, the error reported by a handler is returned to the caller with
// error code 0 and the text of the error as its message. A handler may return
// a value of concrete type [ErrorData] or [*ErrorData] to control the error
// code, message, and auxiliary error data.
type Handler func(context.Context, *Request) ([]byte, error)

// A PacketHandler processes a packet from the remote peer. A packet handler
// can obtain the peer from its context argument using the [ContextPeer]
// helper.  Any error reported by a packet handler is protocol fatal.
type PacketHandler func(context.Context, Packet) error

// A PacketLogger logs a packet exchanged with the remote peer.  The value of
// dir is either [Send] for a packet sent by the local peer, or [Recv] for a
// packet received from the remote peer.
type PacketLogger func(pkt Packet, dir PacketDir)

// PacketDir indicates the "direction" of a packet, either [Send] or [Recv].
type PacketDir string

const (
	Send PacketDir = "send" // a packet sent from local to remote
	Recv PacketDir = "recv" // a packet received by local from remote
)

// A Peer implements a Chirp v0 peer. A zero-valued Peer is ready for use, but
// must not be copied after any method has been called.
//
// Call [Peer.Start] with a channel to start the service routine for the peer.
// Once started, a peer runs until [Peer.Stop] is called, the channel closes,
// or a protocol fatal error occurs. Use [Peer.Wait] to wait for the peer to
// exit and report its status.
//
// Calling [Peer.Stop] terminates all method handlers and outbound calls
// currently executing.
//
// Call [Peer.Handle] to add handlers to the local peer.  Use [Peer.Call] to
// invoke a call on the remote peer. Both of these methods are safe for
// concurrent use by multiple goroutines.
type Peer struct {
	in  interface{ Recv() (Packet, error) }
	out struct {
		// Must hold the lock to send to or set ch.
		sync.Mutex
		ch Channel
	}
	tasks  *taskgroup.Group
	peermx atomic.Pointer[peerMetrics]

	μ sync.Mutex

	err   error                        // protocol fatal error
	ocall map[uint32]pending           // outbound calls pending responses
	nexto uint32                       // next unused outbound call ID
	icall map[uint32]func(error)       // requestID → cancel cause func
	imux  map[string]Handler           // methodName → handler
	pmux  map[PacketType]PacketHandler // packetType → packet handler
	plog  PacketLogger                 // what it says on the tin
	base  func() context.Context       // return a new base context
}

// NewPeer constructs a new unstarted peer.
func NewPeer() *Peer { return new(Peer) }

// Start starts the peer running on the given channel. The peer runs until the
// channel closes or a protocol fatal error occurs. Start does not block; call
// [Peer.Wait] to wait for the peer to exit and report its status.
func (p *Peer) Start(ch Channel) *Peer {
	if p.in != nil {
		panic("peer is already started")
	}

	g := taskgroup.New(nil)
	p.in = ch
	p.tasks = g
	p.out.ch = ch
	p.err = nil
	p.ocall = make(map[uint32]pending)
	p.nexto = 0
	p.icall = make(map[uint32]func(error))
	if p.base == nil {
		// N.B. Don't overwrite this if we still have a value from a previous
		// session on this same peer.
		p.base = context.Background
	}

	g.Go(func() error {
		for {
			pkt, err := p.in.Recv()
			if err != nil {
				p.fail(err)
				return nil
			}
			p.metrics().packetRecv.Add(1)
			if err := p.dispatchPacket(pkt); err != nil {
				p.fail(err)
				return nil
			}
		}
	})

	return p
}

// Metrics returns a metrics map for the peer. It is safe for the caller to add
// additional metrics to the map while the peer is active.
func (p *Peer) Metrics() *expvar.Map { return p.metrics().emap }

// Detach detaches the peer metrics for p, and returns p. Calling Detach resets
// all the metrics for p. Any peers (recursively) cloned from p after detaching
// will share this new metrics pool.
func (p *Peer) Detach() *Peer { p.peermx.Store(newPeerMetrics()); return p }

// metrics returns the peerMetrics target for p.
func (p *Peer) metrics() *peerMetrics {
	if pm := p.peermx.Load(); pm != nil {
		return pm
	}
	return rootMetrics
}

// Clone returns a new unstarted peer that has the same method handlers, packet
// handlers, logger, metrics, and base context function as p.  After cloning,
// further changes to either peer do not affect the other.
func (p *Peer) Clone() *Peer {
	p.μ.Lock()
	defer p.μ.Unlock()
	cp := &Peer{
		imux: maps.Clone(p.imux),
		pmux: maps.Clone(p.pmux),
		plog: p.plog,
		base: p.base,
	}
	cp.peermx.Store(p.peermx.Load())
	return cp
}

// Stop closes the channel and terminates the peer. It blocks until the peer
// has exited and returns its status. After Stop completes it is safe to
// restart the peer with a new channel.
func (p *Peer) Stop() error { p.closeOut(); return p.Wait() }

func treatErrorAsSuccess(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)
}

// waitTasks blocks until the service routines have finished, and reports
// whether the peer was running.
func (p *Peer) waitTasks() bool {
	p.μ.Lock()
	t := p.tasks
	p.μ.Unlock()
	if t == nil {
		return false
	}
	t.Wait()
	return true
}

// Wait blocks until p terminates and reports the error that cause it to stop.
// After Wait completes it is safe to restart the peer with a new channel.
//
// If p is not running, or has stopped because of a closed channel, Wait
// returns nil; otherwise it returns the error that triggered protocol failure.
func (p *Peer) Wait() error {
	if !p.waitTasks() {
		return nil // the peer is not running
	}

	// Clean up peer state so it can be garbage collected.
	p.μ.Lock()
	defer p.μ.Unlock()
	p.in = nil
	p.tasks = nil
	p.out.Lock()
	p.out.ch = nil
	p.out.Unlock()
	p.ocall = nil
	p.icall = nil

	if treatErrorAsSuccess(p.err) {
		return nil
	}
	return p.err
}

// SendPacket sends a packet to the remote peer. Any error is protocol fatal.
// Any packet type can be sent, including reserved types. The caller is
// responsible for ensuring such packets have a valid payload.
func (p *Peer) SendPacket(ptype PacketType, payload []byte) error {
	return p.sendOut(Packet{
		Type:    ptype,
		Payload: payload,
	})
}

// Call sends a call to the remote peer for the specified method and data, and
// blocks until ctx ends or until the response is received. If ctx ends before
// the peer replies, the call will be automatically cancelled.
//
// In case of any error, Call returns a nil [*Response]. The concrete type of
// the error value is [*CallError], and if the error came from the remote peer
// the corresponding response can be recovered from its Response field.
func (p *Peer) Call(ctx context.Context, method string, data []byte) (_ *Response, err error) {
	p.metrics().callOut.Add(1)
	defer func() {
		if err != nil {
			p.metrics().callOutErr.Add(1)
		}
	}()
	if len(method) > MaxMethodLen {
		return nil, callError(fmt.Errorf("method %q name too long (%d bytes > %d)", method, len(method), MaxMethodLen))
	}
	if err := ctx.Err(); err != nil {
		return nil, callError(err) // already ended, don't attempt to forward it
	}
	if isLocalExec(ctx) {
		return p.callLocal(ctx, method, data)
	}
	id, pc, err := p.sendReq(method, data)
	if err != nil {
		return nil, callError(err)
	}
	p.metrics().callPending.Add(1)
	defer p.metrics().callPending.Add(-1)

	done := ctx.Done()
	for {
		select {
		case <-done:
			// The local context ended, push a cancellation to the peer, then
			// resume waiting for the response. Set done to nil so that we will
			// not recur on this case.
			p.sendCancel(id)
			done = nil

			// Set a watchdog timer to ensure the call eventually gives up and
			// reports an error, even if we don't get a reply from the peer.
			ct := time.AfterFunc(100*time.Millisecond, func() {
				p.μ.Lock()
				defer p.μ.Unlock()

				// The call may have completed while we were waiting.
				// If not, however, we do not release the request ID, otherwise a
				// subsequent call may attempt to reuse it and get a spurious
				// duplicate request error because the peer hasn't yet yielded it.
				if pc, ok := p.ocall[id]; ok {
					p.ocall[id] = nil // pin the ID
					pc.deliverLocked(&Response{RequestID: id, Code: CodeCanceled})
				}
			})
			// If the call succeeds before the watchdog expires, cancel it.
			defer ct.Stop()
			continue

		case rsp, ok := <-pc:
			if ok {
				switch rsp.Code {
				case CodeSuccess:
					return rsp, nil
				case CodeCanceled:
					return nil, &CallError{Err: context.Canceled, Response: rsp}
				}
				ce := &CallError{Response: rsp}

				// Try to decode the error data, but if that fails use the string
				// from the failure message so the caller has a way to debug.
				if err := ce.ErrorData.Decode(rsp.Data); err != nil {
					ce.Message = err.Error()
				}
				return nil, ce
			}

			// Closed without a response means there was a protocol fatal error.
			p.tasks.Wait()
			return nil, callError(fmt.Errorf("call terminated: %w", p.err))
		}
	}
}

// resultCoder is an extension interface an error may implement to override the
// result code reported for the error.
type resultCoder interface{ ResultCode() ResultCode }

// ErrUnknownMethod is a sentinel error that a [Handler] may return to cause
// the call to report an unknown method error. This is intended for use in
// wildcard handlers (see [Peer.Handle]), although any handler may return it.
var ErrUnknownMethod errUnknownMethod

// errUnknownMethod is an internal sentinel used to signal an unknown method in
// the Exec method. It is recognized by the dispatch plumbing so that a handler
// reporting it will behave as if no handler was found.
type errUnknownMethod struct{}

func (errUnknownMethod) Error() string          { return "exec: unknown method" }
func (errUnknownMethod) ResultCode() ResultCode { return CodeUnknownMethod }

// errDuplicateRequest is an internal sentinel error used to cancel a pending
// request that shares an ID with another request received later.
var errDuplicateRequest = errors.New("duplicate request ID")

// errPeerCancel is an internal sentinel error used to cancel a pending
// request due to an explicit request from the peer.
var errPeerCancel = errors.New("peer canceled request")

// Exec executes the (local) handler on p for the method, if one exists.
// If no handler is defined for method, Exec reports an internal error with an
// empty result; otherwise it returns the result of calling the handler with
// the given data.
//
// If the handler for the specified method itself executes [Peer.Call], the
// call will dispatch to p itself, as if calling [Peer.Exec].
func (p *Peer) Exec(ctx context.Context, method string, data []byte) ([]byte, error) {
	if ContextPeer(ctx) == nil {
		ctx = context.WithValue(ctx, peerContextKey{}, p)
	}
	rsp, err := p.Call(context.WithValue(ctx, execContextKey{}, true), method, data)
	if err != nil {
		return nil, err
	}
	return rsp.Data, nil
}

// Handle registers a handler for the specified method name. It is safe to call
// this while the peer is running. Passing a nil [Handler] removes any handler
// for the specified method. Handle returns p to permit chaining.
//
// As a special case, if method == "" the handler is called for any request
// with a method name that does not have a more specific handler registered.
// If len(method) > [MaxMethodLen], Handle will panic.
func (p *Peer) Handle(method string, handler Handler) *Peer {
	if len(method) > MaxMethodLen {
		panic(fmt.Sprintf("method %q name too long (%d bytes > %d)", method, len(method), MaxMethodLen))
	}
	p.μ.Lock()
	defer p.μ.Unlock()
	if p.imux == nil {
		p.imux = make(map[string]Handler)
	}
	if handler == nil {
		delete(p.imux, method)
	} else {
		p.imux[method] = handler
	}
	return p
}

// HandlePacket registers a callback that will be invoked whenever the remote
// peer sends a packet with the specified type. This method will panic if a
// reserved packet type is specified. Passing a nil callback removes any
// handler for the specified packet type. HandlePacket returns p to permit
// chaining.
//
// Packet handlers are invoked synchronously with the processing of packets
// sent by the remote peer, and there will be at most one packet handler active
// at a time. If a packet handler panics or reports an error, it is protocol
// fatal and will terminate the peer.
func (p *Peer) HandlePacket(ptype PacketType, handler PacketHandler) *Peer {
	if ptype <= maxReservedType {
		panic(fmt.Sprintf("cannot handle reserved packet type %d", ptype))
	}

	p.μ.Lock()
	defer p.μ.Unlock()
	if p.pmux == nil {
		p.pmux = make(map[PacketType]PacketHandler)
	}
	if handler == nil {
		delete(p.pmux, ptype)
	} else {
		p.pmux[ptype] = handler
	}
	return p
}

// LogPackets registers a callback that will be invoked for each packet
// exchanged with the remote peer, regardless of type, including packets to be
// discarded.
//
// Passing a nil callback disables packet logging. The packet logger is invoked
// synchronously with dispatch, prior to sending or calling a packet handler.
func (p *Peer) LogPackets(log PacketLogger) *Peer {
	p.μ.Lock()
	defer p.μ.Unlock()
	p.plog = log
	return p
}

// NewContext registers a function that will be called to create a new base
// context for method and packet handlers. This allows request-specific host
// resources to be plumbed into a handler.  If it is not set a background
// context is used.
func (p *Peer) NewContext(base func() context.Context) *Peer {
	p.μ.Lock()
	defer p.μ.Unlock()
	if base == nil {
		p.base = context.Background
	} else {
		p.base = base
	}
	return p
}

// RemoteAddr reports the [net.Addr] of the remote peer. It returns nil if p is
// not running, or if the [Channel] connecting p to the peer does not have a
// recoverable address.
func (p *Peer) RemoteAddr() net.Addr {
	p.out.Lock()
	defer p.out.Unlock()
	if nc, ok := p.out.ch.(netConner); ok {
		if c := nc.NetConn(); c != nil {
			return c.RemoteAddr()
		}
	}
	return nil
}

// netConner is the interface exported by [Channel] implementations that may
// have an underlying [net.Conn] and know now to report it. The method may
// return nil if a conn is not available.
type netConner interface{ NetConn() net.Conn }

// fail terminates all pending calls and updates the failure status.
func (p *Peer) fail(err error) {
	p.closeOut()

	p.μ.Lock()
	defer p.μ.Unlock()

	// Terminate all incomplete pending (outbound) calls.
	for _, pc := range p.ocall {
		pc.close()
	}
	p.ocall = nil

	// Terminate all incomplete active (inbound) calls.
	for _, stop := range p.icall {
		stop(nil)
	}
	p.icall = nil
	p.err = err
}

func (p *Peer) sendRsp(rsp *Response) {
	p.μ.Lock()
	delete(p.icall, rsp.RequestID)
	err := p.err
	p.μ.Unlock()

	if err != nil {
		return
	}

	if err := p.sendOut(Packet{
		Type:    PacketResponse,
		Payload: rsp.Encode(),
	}); err != nil {
		p.closeOut()
	}
}

// callLocal invokes a method handler defined on p itself, rather than
// forwarding it to the remote peer.
//
// Unlike a regular peer Call, local calls dispatch directly to the assigned
// handler, and do not send messages over the channel or acquire request IDs.
func (p *Peer) callLocal(ctx context.Context, method string, data []byte) (*Response, error) {
	handler, err := func() (Handler, error) {
		p.μ.Lock()
		defer p.μ.Unlock()
		if err := p.err; err != nil {
			return nil, err
		}
		handler, ok := p.imux[method]
		if !ok {
			const wildcardID = ""
			handler, ok = p.imux[wildcardID]
			if !ok {
				return nil, errUnknownMethod{}
			}
		}
		return handler, nil
	}()
	if rc, ok := err.(resultCoder); ok { // most likely errUnknownMethod, see above
		return nil, &CallError{Response: &Response{Code: rc.ResultCode()}}
	} else if err != nil {
		return nil, callError(err)
	}

	p.metrics().callPending.Add(1)
	defer p.metrics().callPending.Add(-1)
	result, err := handler(ctx, &Request{
		Method: method,
		Data:   data,
	})
	if err != nil {
		// Propagate context cancellation as a local error, rather than
		// decorating it as a response from the remote peer, so the caller can
		// check for it in the usual way.
		if ctx.Err() != nil {
			return nil, callError(ctx.Err())
		}
		if ce := (*CallError)(nil); errors.As(err, &ce) {
			return nil, err
		}

		ce := &CallError{Response: &Response{Code: CodeServiceError}}
		if ed, ok := err.(*ErrorData); ok {
			ce.ErrorData = *ed
		} else if ed, ok := err.(ErrorData); ok {
			ce.ErrorData = ed
		} else {
			ce.ErrorData = ErrorData{Message: err.Error()}
		}
		return nil, ce
	}
	return &Response{Code: CodeSuccess, Data: result}, nil
}

// sendReq sends a request packet for the given method and data.
// It blocks until the send completes, but does not wait for the reply.
// It returns the assigned request ID.
// The response will be delivered on the returned pending channel.
func (p *Peer) sendReq(method string, data []byte) (uint32, pending, error) {
	// Phase 1: Check for fatal errors and acquire state.
	p.μ.Lock()
	defer p.μ.Unlock()
	if err := p.err; err != nil {
		return 0, nil, err
	} else if p.ocall == nil {
		panic("peer is not started")
	}
	p.nexto++
	id := p.nexto
	pc := make(pending, 1)
	p.ocall[id] = pc

	// Send the request to the remote peer. Note we MUST NOT hold the state lock
	// while doing this, as that will block the receiver from dispatching packets.
	p.μ.Unlock()
	err := func() error {
		defer p.μ.Lock()
		return p.sendOut(Packet{
			Type: PacketRequest,
			Payload: Request{
				RequestID: id,
				Method:    method,
				Data:      data,
			}.Encode(),
		})
	}()

	// Phase 2: Check for an error in the send, and update state if it failed.
	if err != nil {
		p.releaseIDLocked(id)
		return 0, nil, err
	}
	return id, pc, nil
}

// sendCancel sends a cancellation for id to the remote peer then returns the
// error from ctx.
func (p *Peer) sendCancel(id uint32) {
	if err := p.sendOut(Packet{
		Type:    PacketCancel,
		Payload: Cancel{RequestID: id}.Encode(),
	}); err != nil {
		p.closeOut() // protocol fatal
	}
}

// dispatchRequestLocked dispatches an inbound request to its handler.
// It reports an error back to the caller for duplicate request ID or unknown method.
func (p *Peer) dispatchRequestLocked(req *Request) (err error) {
	p.metrics().callIn.Add(1)
	defer func() {
		if err != nil {
			p.metrics().callInErr.Add(1)
		}
	}()

	// Report duplicate request ID and terminate the existing call.
	if stop, ok := p.icall[req.RequestID]; ok {
		stop(errDuplicateRequest)
		return p.sendOut(Packet{
			Type: PacketResponse,
			Payload: Response{
				RequestID: req.RequestID,
				Code:      CodeDuplicateID,
			}.Encode(),
		})
	}

	handler, ok := p.imux[req.Method]
	if !ok {
		const wildcardID = ""
		// Check whether a wildcard handler is registered.
		if wc, ok := p.imux[wildcardID]; ok {
			handler = wc
		} else {
			return p.sendOut(Packet{
				Type: PacketResponse,
				Payload: Response{
					RequestID: req.RequestID,
					Code:      CodeUnknownMethod,
				}.Encode(),
			})
		}
	}

	// Start a goroutine to service the request. The goroutine handles
	// cancellation and response delivery.
	pctx := context.WithValue(p.base(), peerContextKey{}, p)
	ctx, cancelCause := context.WithCancelCause(pctx)
	p.icall[req.RequestID] = cancelCause
	p.metrics().callActive.Add(1)

	p.tasks.Run(func() {
		defer cancelCause(nil)
		defer p.metrics().callActive.Add(-1)

		data, err := func() (_ []byte, err error) {
			// Ensure a panic out of the handler is turned into a graceful response.
			defer func() {
				if x := recover(); x != nil {
					err = ErrorData{
						Message: fmt.Sprintf("handler panicked (recovered): %v", x),
						Data:    debug.Stack(),
					}
				}
			}()
			return handler(ctx, req)
		}()

		rsp := &Response{RequestID: req.RequestID}
		if cerr := context.Cause(ctx); errors.Is(cerr, errPeerCancel) {
			rsp.Code = CodeCanceled
		} else if errors.Is(cerr, errDuplicateRequest) {
			rsp.Code = CodeDuplicateID
		} else if err == nil {
			rsp.Code = CodeSuccess
			rsp.Data = data
		} else if rc, ok := err.(resultCoder); ok {
			rsp.Code = rc.ResultCode()
			rsp.Data = data
		} else if ed, ok := err.(*ErrorData); ok {
			rsp.Code = CodeServiceError
			rsp.Data = ed.Encode()
		} else if ed, ok := err.(ErrorData); ok {
			rsp.Code = CodeServiceError
			rsp.Data = ed.Encode()
		} else if ce := (*CallError)(nil); errors.As(err, &ce) && ce.Response != nil {
			// This may occur if we are handling an outbound call on behalf of a
			// local caller via Exec.
			rsp.Code = ce.Response.Code
			rsp.Data = ce.ErrorData.Encode()
		} else {
			rsp.Code = CodeServiceError
			rsp.Data = ErrorData{Message: err.Error()}.Encode()
		}
		p.sendRsp(rsp)
	})
	return nil
}

// dispatchPacket routes an inbound packet from the remote peer.
// Any error it reports is protocol fatal.
func (p *Peer) dispatchPacket(pkt Packet) error {
	p.logPacket(pkt, Recv)
	if pkt.Protocol != 0 {
		return nil // we understand only v0 packets
	}

	switch pkt.Type {
	case PacketRequest:
		var req Request
		if err := req.Decode(pkt.Payload); err != nil {
			return fmt.Errorf("invalid request packet: %w", err)
		}
		p.μ.Lock()
		defer p.μ.Unlock()
		return p.dispatchRequestLocked(&req)

	case PacketCancel:
		var req Cancel
		if err := req.Decode(pkt.Payload); err != nil {
			return fmt.Errorf("invalid cancel packet: %w", err)
		}
		p.metrics().cancelIn.Add(1)
		p.μ.Lock()
		defer p.μ.Unlock()

		// If there is a dispatch in flight for this request, signal it to stop.
		// The dispatch wrapper will figure out how to reply and clean up.
		if stop, ok := p.icall[req.RequestID]; ok {
			stop(errPeerCancel)
		}
		return nil

	case PacketResponse:
		var rsp Response
		if err := rsp.Decode(pkt.Payload); err != nil {
			return fmt.Errorf("invalid response packet: %w", err)
		}
		p.μ.Lock()
		defer p.μ.Unlock()

		pc, ok := p.ocall[rsp.RequestID]
		if !ok {
			// Silently discard response for unknown request ID.
			return nil
		}

		p.releaseIDLocked(rsp.RequestID)
		pc.deliverLocked(&rsp) // does not block

	default:
		p.μ.Lock()
		handler, ok := p.pmux[pkt.Type]
		p.μ.Unlock()
		if !ok {
			p.metrics().packetDropped.Add(1)
			break // ignore the packet
		}

		pctx := context.WithValue(p.base(), peerContextKey{}, p)
		return func() (err error) {
			// Ensure a panic out of a packet handler is turned into a protocol fatal.
			defer func() {
				if x := recover(); x != nil {
					err = fmt.Errorf("packet handler panicked (recovered): %v", x)
				}
			}()
			return handler(pctx, pkt)
		}()
	}
	return nil
}

func (p *Peer) logPacket(pkt Packet, dir PacketDir) {
	if p.plog != nil {
		p.plog(pkt, dir)
	}
}

// releaseID releases the call state for the specified outbound request id.
func (p *Peer) releaseIDLocked(id uint32) {
	delete(p.ocall, id)
	if len(p.ocall) == 0 {
		p.nexto = 0
	}
}

func (p *Peer) sendOut(pkt Packet) error {
	p.out.Lock()
	defer p.out.Unlock()
	if p.out.ch == nil {
		panic("peer is not started")
	}
	p.metrics().packetSent.Add(1)
	p.logPacket(pkt, Send)
	return p.out.ch.Send(pkt)
}

func (p *Peer) closeOut() {
	p.out.Lock()
	defer p.out.Unlock()
	if p.out.ch != nil {
		p.out.ch.Close()
	}
}

type pending chan *Response

func (p pending) close() {
	if p != nil {
		close(p)
	}
}

func (p pending) deliverLocked(r *Response) {
	if p != nil {
		p <- r
		close(p)
	}
}

func callError(err error) *CallError { return &CallError{Err: err} }

// CallError is the concrete type of errors reported by the [Peer.Call] method.
// For service errors, the Err field is nil and the [ErrorData] contains the
// error details. For errors arising from a response, the Response field
// contains the complete response message.
type CallError struct {
	ErrorData
	Err      error     // nil for service errors
	Response *Response // set if a the error came from a call response
}

// Unwrap reports the underlying error of c. If c.Err == nil, this is nil.
func (c *CallError) Unwrap() error { return c.Err }

// Error satisfies the error interface.
func (c *CallError) Error() string {
	if c.Err != nil {
		return c.Err.Error()
	} else if c.Response.Code == CodeServiceError {
		return fmt.Sprintf("service error: %v", c.ErrorData.Error())
	}
	return fmt.Sprintf("request %d: %s", c.Response.RequestID, c.Response.Code.String())
}

type peerContextKey struct{}

// ContextPeer returns the [Peer] associated with the given context, or nil if
// none is defined.  The context passed to a method Handler has this value.
func ContextPeer(ctx context.Context) *Peer {
	if v := ctx.Value(peerContextKey{}); v != nil {
		return v.(*Peer)
	}
	return nil
}

type execContextKey struct{}

func isLocalExec(ctx context.Context) bool {
	if isLocal, ok := ctx.Value(execContextKey{}).(bool); ok {
		return isLocal
	}
	return false
}

// SplitAddress parses an address string to guess a network type and target.
//
// The assignment of a network type uses the following heuristics:
//
// If s does not have the form [host]:port, the network is assigned as "tcp" if
// the address is numeric; otherwise it is assigned "unix".  The network "unix"
// is also assigned if port == "", if port contains characters other than ASCII
// letters, digits, and "-", or if host contains a "/".
//
// Otherwise, the network is assigned as "tcp". Note that this function does
// not verify whether the address is lexically valid.
func SplitAddress(s string) (network, address string) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		if _, err := strconv.Atoi(s); err == nil {
			return "tcp", s
		}
		return "unix", s
	}
	host, port := s[:i], s[i+1:]
	if port == "" || !isServiceName(port) {
		return "unix", s
	} else if strings.IndexByte(host, '/') >= 0 {
		return "unix", s
	}
	return "tcp", s
}

// isServiceName reports whether s looks like a legal service name from the
// services(5) file. The grammar of such names is not well-defined, but for our
// purposes it includes letters, digits, and "-".
func isServiceName(s string) bool {
	for _, b := range s {
		if b >= '0' && b <= '9' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b == '-' {
			continue
		}
		return false
	}
	return true
}
