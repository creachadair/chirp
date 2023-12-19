// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

package chirp

import (
	"context"
	"errors"
	"expvar"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/creachadair/taskgroup"
)

// A Channel is a reliable ordered stream of packets shared by two peers.
//
// The methods of an implementation must be safe for concurrent use by one
// sender and one receiver.
type Channel interface {
	// Send the packet in binary format to the receiver.
	Send(*Packet) error

	// Receive the next available packet from the channel.
	Recv() (*Packet, error)

	// Close the channel, causing any pending send or receive operations to
	// terminate and report an error. After a channel is closed, all further
	// operations on it must report an error.
	Close() error
}

// A Handler processes a request from the remote peer.  A handler can obtain
// the peer from its context argument using the ContextPeer helper.
//
// By default, the error reported by a handler is returned to the caller with
// error code 0 and the text of the error as its message. A handler may return
// a value of concrete type ErrorData or *ErrorData to control the error code,
// message, and auxiliary error data.
type Handler func(context.Context, *Request) ([]byte, error)

// A PacketHandler processes a packet from the remote peer. A packet handler
// can obtain the peer from its context argument using the ContextPeer helper.
// Any error reported by a packet handler is protocol fatal.
type PacketHandler func(context.Context, *Packet) error

// A PacketLogger logs a packet exchanged with the remote peer.
type PacketLogger func(pkt PacketInfo)

// A PacketInfo combines a packet and a flag indicating whether the packet was
// sent or received.
type PacketInfo struct {
	*Packet      // the packet being logged
	Sent    bool // whether the packet was sent (true) or received (false)
}

func (p PacketInfo) dir() string {
	if p.Sent {
		return "send"
	}
	return "recv"
}

func (p PacketInfo) String() string {
	return fmt.Sprintf("%v %v", p.dir(), p.Packet)
}

// A Peer implements a Chirp v0 peer. A zero-valued Peer is ready for use, but
// must not be copied after any method has been called.
//
// Call Start with a channel to start the service routine for the peer.  Once
// started, a peer runs until Stop is called, the channel closes, or a protocol
// fatal error occurs. Use Wait to wait for the peer to exit and report its
// status.
//
// Calling Stop terminates all method handlers and calls currently executing.
//
// Call Handle to add handlers to the local peer.  Use Call to invoke a call on
// the remote peer. Both of these methods are safe for concurrent use by
// multiple goroutines.
type Peer struct {
	in  interface{ Recv() (*Packet, error) }
	out struct {
		// Must hold the lock to send to or set ch.
		sync.Mutex
		ch Channel
	}
	tasks *taskgroup.Group

	μ sync.Mutex

	err   error                        // protocol fatal error
	ocall map[uint32]pending           // outbound calls pending responses
	nexto uint32                       // next unused outbound call ID
	icall map[uint32]func()            // requestID → cancel func
	imux  map[uint32]Handler           // methodID → handler
	pmux  map[PacketType]PacketHandler // packetType → packet handler
	plog  PacketLogger                 // what it says on the tin
	base  func() context.Context       // return a new base context

	onExit func(error)
}

// NewPeer constructs a new unstarted peer.
func NewPeer() *Peer { return new(Peer) }

// Start starts the peer running on the given channel. The peer runs until the
// channel closes or a protocol fatal error occurs. Start does not block; call
// Wait to wait for the peer to exit and report its status.
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
	p.icall = make(map[uint32]func())
	p.base = context.Background

	g.Go(func() error {
		for {
			pkt, err := p.in.Recv()
			if err != nil {
				p.fail(err)
				return nil
			}
			peerMetrics.packetRecv.Add(1)
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
func (p *Peer) Metrics() *expvar.Map { return peerMetrics.emap }

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
	return p.sendOut(&Packet{
		Type:    ptype,
		Payload: payload,
	})
}

// Call sends a call to the remote peer for the specified method and data, and
// blocks until ctx ends or until the response is received. If ctx ends before
// the peer replies, the call will be automatically cancelled.  An error
// reported by Call has concrete type *CallError.
func (p *Peer) Call(ctx context.Context, method uint32, data []byte) (_ *Response, err error) {
	peerMetrics.callOut.Add(1)
	defer func() {
		if err != nil {
			peerMetrics.callOutErr.Add(1)
		}
	}()

	id, pc, err := p.sendReq(method, data)
	if err != nil {
		return nil, callError(err)
	}
	peerMetrics.callPending.Add(1)
	defer peerMetrics.callPending.Add(-1)

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
			ct := time.AfterFunc(50*time.Millisecond, func() {
				p.μ.Lock()
				defer p.μ.Unlock()

				// The call may have completed while we were waiting.
				// If not, however, we do not release the request ID, otherwise a
				// subsequent call may attempt to reuse it and get a spurious
				// duplicate request error because the peer hasn't yet yielded it.
				if pc, ok := p.ocall[id]; ok {
					p.ocall[id] = nil // pin the ID
					pc.deliver(&Response{RequestID: id, Code: CodeCanceled})
				}
			})
			// If the call succeeds before the watchdog expires, cancel it.
			defer ct.Stop()
			continue

		case rsp, ok := <-pc:
			if ok {
				if rsp.Code == CodeSuccess {
					return rsp, nil
				} else if rsp.Code == CodeCanceled {
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

// errUnknownMethod is an internal sentinel used to signal an unknown method in
// the Exec method. It is recognized by the dispatch plumbing so that a handler
// reporting it will behave as if no handler was found.
type errUnknownMethod struct{}

func (errUnknownMethod) Error() string          { return "exec: unknown method" }
func (errUnknownMethod) ResultCode() ResultCode { return CodeUnknownMethod }

// Exec executes the (local) handler on p for the methodID, if one exists.  If
// no handler is defined for methodID, Exec reports an internal error with an
// empty result; otherwise it returns the result of calling the handler with
// the given data.
func (p *Peer) Exec(ctx context.Context, methodID uint32, data []byte) ([]byte, error) {
	p.μ.Lock()
	handler, ok := p.imux[methodID]
	p.μ.Unlock()
	if !ok {
		return nil, errUnknownMethod{}
	}
	return handler(ctx, &Request{MethodID: methodID, Data: data})
}

// Handle registers a handler for the specified method ID. It is safe to call
// this while the peer is running. Passing a nil Handler removes any handler
// for the specified ID. Handle returns p to permit chaining.
//
// As a special case, if methodID == 0 the handler is called for any request
// with a method ID that does not have a more specific handler registered.
func (p *Peer) Handle(methodID uint32, handler Handler) *Peer {
	p.μ.Lock()
	defer p.μ.Unlock()
	if p.imux == nil {
		p.imux = make(map[uint32]Handler)
	}
	if handler == nil {
		delete(p.imux, methodID)
	} else {
		p.imux[methodID] = handler
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

// OnExit registers a callback to be invoked when the peer terminates.  The
// callback is executed synchronously during shutdown, with the same error
// value that would be reported by the Wait method.
//
// Only one exit callback can be registered at a time; if f == nil the callback
// is removed.
func (p *Peer) OnExit(f func(error)) *Peer {
	p.μ.Lock()
	defer p.μ.Unlock()
	p.onExit = f
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
		stop()
	}
	p.icall = nil

	p.err = err
	if p.onExit != nil {
		if treatErrorAsSuccess(err) {
			err = nil
		}
		p.onExit(err)
	}
}

func (p *Peer) sendRsp(rsp *Response) {
	p.μ.Lock()
	delete(p.icall, rsp.RequestID)
	err := p.err
	p.μ.Unlock()

	if err != nil {
		return
	}

	if err := p.sendOut(&Packet{
		Type:    PacketResponse,
		Payload: rsp.Encode(),
	}); err != nil {
		p.closeOut()
	}
}

// sendReq sends a request packet for the given method and data.
// It blocks until the send completes, but does not wait for the reply.
// The response will be delivered on the returned pending channel.
func (p *Peer) sendReq(method uint32, data []byte) (uint32, pending, error) {
	// Phase 1: Check for fatal errors and acquire state.
	p.μ.Lock()
	if err := p.err; err != nil {
		p.μ.Unlock()
		return 0, nil, err
	}
	p.nexto++
	id := p.nexto
	pc := make(pending, 1)
	p.ocall[id] = pc
	p.μ.Unlock()

	// Send the request to the remote peer. Note we MUST NOT hold the state lock
	// while doing this, as that will block the receiver from dispatching packets.
	err := p.sendOut(&Packet{
		Type: PacketRequest,
		Payload: Request{
			RequestID: id,
			MethodID:  method,
			Data:      data,
		}.Encode(),
	})

	// Phase 2: Check for an error in the send, and update state if it failed.
	p.μ.Lock()
	defer p.μ.Unlock()
	if err != nil {
		p.releaseIDLocked(id)
		return 0, nil, err
	}
	return id, pc, nil
}

// sendCancel sends a cancellation for id to the remote peer then returns the
// error from ctx.
func (p *Peer) sendCancel(id uint32) {
	if err := p.sendOut(&Packet{
		Type:    PacketCancel,
		Payload: Cancel{RequestID: id}.Encode(),
	}); err != nil {
		p.closeOut() // protocol fatal
	}
}

// dispatchRequestLocked dispatches an inbound request to its handler.
// It reports an error back to the caller for duplicate request ID or unknown method.
func (p *Peer) dispatchRequestLocked(req *Request) (err error) {
	peerMetrics.callIn.Add(1)
	defer func() {
		if err != nil {
			peerMetrics.callInErr.Add(1)
		}
	}()

	// Report duplicate request ID without failing the existing call.
	if _, ok := p.icall[req.RequestID]; ok {
		return p.sendOut(&Packet{
			Type: PacketResponse,
			Payload: Response{
				RequestID: req.RequestID,
				Code:      CodeDuplicateID,
			}.Encode(),
		})
	}

	handler, ok := p.imux[req.MethodID]
	if !ok {
		const wildcardID = 0
		// Check whether a wildcard handler is registered.
		if wc, ok := p.imux[wildcardID]; ok {
			handler = wc
		} else {
			return p.sendOut(&Packet{
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
	ctx, cancel := context.WithCancel(pctx)
	p.icall[req.RequestID] = cancel
	peerMetrics.callActive.Add(1)

	p.tasks.Go(func() error {
		defer cancel()
		defer peerMetrics.callActive.Add(-1)

		data, err := func() (_ []byte, err error) {
			// Ensure a panic out of the handler is turned into a graceful response.
			defer func() {
				if x := recover(); x != nil && err == nil {
					err = fmt.Errorf("handler panicked (recovered): %v", x)
				}
			}()
			return handler(ctx, req)
		}()

		rsp := &Response{RequestID: req.RequestID}
		if ctx.Err() != nil || err == context.Canceled || err == context.DeadlineExceeded {
			// N.B. Only do this for the unwrapped sentinel errors.

			// If the context terminated, treat this as a cancellation even if the
			// handler succeeded. This usually means the context timed out or the
			// remote peer sent a cancellation that the handler ignored.

			rsp.Code = CodeCanceled
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
		} else {
			rsp.Code = CodeServiceError
			rsp.Data = ErrorData{Message: err.Error()}.Encode()
		}
		p.sendRsp(rsp)
		return nil
	})
	return nil
}

// dispatchPacket routes an inbound packet from the remote peer.
// Any error it reports is protocol fatal.
func (p *Peer) dispatchPacket(pkt *Packet) error {
	if p.plog != nil {
		p.plog(PacketInfo{Packet: pkt, Sent: false})
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
		peerMetrics.cancelIn.Add(1)
		p.μ.Lock()
		defer p.μ.Unlock()

		// If there is a dispatch in flight for this request, signal it to stop.
		// The dispatch wrapper will figure out how to reply and clean up.
		if stop, ok := p.icall[req.RequestID]; ok {
			stop()
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
		pc.deliver(&rsp) // does not block

	default:
		p.μ.Lock()
		handler, ok := p.pmux[pkt.Type]
		p.μ.Unlock()
		if !ok {
			peerMetrics.packetDropped.Add(1)
			break // ignore the packet
		}

		pctx := context.WithValue(p.base(), peerContextKey{}, p)
		return func() (err error) {
			// Ensure a panic out of a packet handler is turned into a protocol fatal.
			defer func() {
				if x := recover(); x != nil && err == nil {
					err = fmt.Errorf("packet handler panicked (recovered): %v", x)
				}
			}()
			return handler(pctx, pkt)
		}()
	}
	return nil
}

// releaseID releases the call state for the specified outbound request id.
func (p *Peer) releaseIDLocked(id uint32) {
	delete(p.ocall, id)
	if len(p.ocall) == 0 {
		p.nexto = 0
	}
}

func (p *Peer) sendOut(pkt *Packet) error {
	p.out.Lock()
	defer p.out.Unlock()
	peerMetrics.packetSent.Add(1)
	if p.plog != nil {
		p.plog(PacketInfo{Packet: pkt, Sent: true})
	}
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

func (p pending) deliver(r *Response) {
	if p != nil {
		p <- r
		close(p)
	}
}

func callError(err error) *CallError { return &CallError{Err: err} }

// CallError is the concrete type of errors reported by the Call method of a
// Peer. For service errors, the Err field is nil and the ErrorData contains
// the error details. For errors arising from a response, the Response field
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

// ContextPeer returns the Peer associated with the given context, or nil if
// none is defined.  The context passed to a method Handler has this value.
func ContextPeer(ctx context.Context) *Peer {
	if v := ctx.Value(peerContextKey{}); v != nil {
		return v.(*Peer)
	}
	return nil
}

// SplitAddress parses an address string to guess a network type and target.
//
// The assignment of a network type uses the following heuristics:
//
// If s does not have the form [host]:port, the network is assigned as "unix".
// The network "unix" is also assigned if port == "", port contains characters
// other than ASCII letters, digits, and "-", or if host contains a "/".
//
// Otherwise, the network is assigned as "tcp". Note that this function does
// not verify whether the address is lexically valid.
func SplitAddress(s string) (network, address string) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
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
