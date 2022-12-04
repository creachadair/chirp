package chirp

import (
	"context"
	"errors"
	"expvar"
	"fmt"
	"io"
	"net"
	"sync"

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
type Handler = func(context.Context, *Request) ([]byte, error)

// A PacketHandler processes a packet from the remote peer. A packet handler
// can obtain the peer from its context argument using the ContextPeer helper.
// Any error reported by a packet handler is protocol fatal.
type PacketHandler = func(context.Context, *Packet) error

// A PacketLogger logs a packet received the remote peer.
type PacketLogger = func(pkt *Packet)

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
// has exited and returns its status.
func (p *Peer) Stop() error { p.closeOut(); return p.Wait() }

// Wait blocks until p terminates and reports the error that cause it to stop.
// After Wait completes it is safe to restart the peer with a new channel.
func (p *Peer) Wait() error {
	if p.tasks == nil {
		return nil
	}
	p.tasks.Wait() // service routine has exited

	if errors.Is(p.err, io.EOF) || errors.Is(p.err, net.ErrClosed) {
		return nil
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

// Call sends a call for the specified method and data and blocks until ctx
// ends or until the response is received. If ctx ends before the peer replies,
// the call will be automatically cancelled.  An error reported by Call has
// concrete type *CallError.
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
			continue

		case rsp, ok := <-pc:
			if ok {
				if rsp.Code == CodeSuccess {
					return rsp, nil
				} else if rsp.Code == CodeCanceled {
					return nil, &CallError{Err: context.Canceled, rsp: rsp}
				}
				ce := &CallError{rsp: rsp}
				if err := ce.ErrorData.UnmarshalBinary(rsp.Data); err != nil {
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

// Handle registers a handler for the specified method ID. It is safe to call
// this while the peer is running. Passing a nil Handler removes any handler
// for the specified ID. Handle returns p to permit chaining.
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

// LogPacket registers a callback that will be invoked for all packets received
// from the remote peer, regardless of type, including packets to be discarded.
//
// Passing a nil callback disables logging. The packet logger is invoked
// synchronously with the processing of packets, prior to handling.
func (p *Peer) LogPacket(log PacketLogger) *Peer {
	p.μ.Lock()
	defer p.μ.Unlock()
	p.plog = log
	return p
}

// NewContext registers a function that will be called to create a new base
// context for method handlers. If it is not set a background context is used.
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
		close(pc)
	}
	p.ocall = nil
	peerMetrics.callPending.Set(0)

	// Terminate all incomplete active (inbound) calls.
	for _, stop := range p.icall {
		stop()
	}
	p.icall = nil
	peerMetrics.callActive.Set(0)

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
		p.releaseID(id)
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

// dispatchRequest dispatches an inbound request to its handler. It reports an
// error back to the caller for duplicate request ID or unknown method.
// The caller must hold p.μ.
func (p *Peer) dispatchRequest(req *Request) (err error) {
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
		return p.sendOut(&Packet{
			Type: PacketResponse,
			Payload: Response{
				RequestID: req.RequestID,
				Code:      CodeUnknownMethod,
			}.Encode(),
		})
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
		if err == nil {
			rsp.Code = CodeSuccess
			rsp.Data = data
		} else if err == context.Canceled || err == context.DeadlineExceeded {
			// N.B. Only do this for the unwrapped sentinel errors.
			rsp.Code = CodeCanceled
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
// The caller must hold p.μ.
func (p *Peer) dispatchPacket(pkt *Packet) error {
	if p.plog != nil {
		p.plog(pkt)
	}
	switch pkt.Type {
	case PacketRequest:
		var req Request
		if err := req.UnmarshalBinary(pkt.Payload); err != nil {
			return fmt.Errorf("invalid request packet: %w", err)
		}
		p.μ.Lock()
		defer p.μ.Unlock()
		return p.dispatchRequest(&req)

	case PacketCancel:
		var req Cancel
		if err := req.UnmarshalBinary(pkt.Payload); err != nil {
			return fmt.Errorf("invalid cancel packet: %w", err)
		}
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
		if err := rsp.UnmarshalBinary(pkt.Payload); err != nil {
			return fmt.Errorf("invalid response packet: %w", err)
		}
		p.μ.Lock()
		defer p.μ.Unlock()

		pc, ok := p.ocall[rsp.RequestID]
		if !ok {
			// Silently discard response for unknown request ID.
			return nil
		}

		p.releaseID(rsp.RequestID)
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
// The caller must hold p.μ.
func (p *Peer) releaseID(id uint32) {
	delete(p.ocall, id)
	if len(p.ocall) == 0 {
		p.nexto = 0
	}
}

func (p *Peer) sendOut(pkt *Packet) error {
	p.out.Lock()
	defer p.out.Unlock()
	peerMetrics.packetSent.Add(1)
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

func (p pending) deliver(r *Response) { p <- r; close(p) }

func callError(err error) *CallError { return &CallError{Err: err} }

// CallError is the concrete type of errors reported by the Call method of a
// Peer.  For a protocol fatal error, the Err field gives the underlying error
// that caused the failure. Otherwise, Err is nil and the ErrorData field has
// the error details reported by the peer.
type CallError struct {
	ErrorData
	Err error // nil for service errors

	rsp *Response
}

// Response returns the response returned by the peer that provided the service
// error details.  If c.Err != nil, this is nil.
func (c *CallError) Response() *Response { return c.rsp }

// Unwrap reports the underlying error of c. If c.Err == nil, this is nil.
func (c *CallError) Unwrap() error { return c.Err }

// Error satisfies the error interface.
func (c *CallError) Error() string {
	if c.Err != nil {
		return c.Err.Error()
	} else if c.rsp.Code == CodeServiceError {
		return fmt.Sprintf("service error: %v", c.ErrorData.Error())
	}
	return fmt.Sprintf("request %d: %s", c.rsp.RequestID, c.rsp.Code.String())
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
