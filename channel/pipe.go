// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

package channel

import (
	"context"
	"net"
	"os"
	"sync"

	"github.com/creachadair/chirp"
)

// NewPipe establishes a connected pair of [os.Pipe] for a pair of peers, and
// returns [Pipe] values wrapping them.
// Sends to A are received by B and vice versa.
// Each call to Pipe returns a distinct pair of pipes.
func NewPipe() (A, B *Pipe, _ error) {
	sr, cw, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	cr, sw, err := os.Pipe()
	if err != nil {
		sr.Close()
		cw.Close()
		return nil, nil, err
	}
	return ConnectPipe(sr, sw), ConnectPipe(cr, cw), nil
}

type recv struct {
	pkt chirp.Packet
	err error
}

// ConnectPipe constructs a new [Pipe] wrapping the specified files.
func ConnectPipe(r, w *os.File) *Pipe {
	ctx, cancel := context.WithCancel(context.Background()) // cancel called by Close
	return &Pipe{
		ctx:    ctx,
		cancel: cancel,
		r:      r,
		w:      w,
		ch:     IO(r, w),
	}
}

// A Pipe implements the [chirp.Channel] interface around an [os.Pipe].
type Pipe struct {
	ctx    context.Context
	cancel context.CancelFunc
	setup  sync.Once
	r, w   *os.File
	ch     IOChannel

	req chan<- struct{}
	rsp <-chan recv
}

// Files returns the underlying [os.File] values used by c.
func (c *Pipe) Files() (r, w *os.File) { return c.r, c.w }

// Send implements part of the [chirp.Channel] interface.
func (c *Pipe) Send(pkt chirp.Packet) error {
	if c.ctx.Err() != nil {
		return net.ErrClosed
	}
	return c.ch.Send(pkt)
}

// Recv implements part of the [chirp.Channel] interface.
func (c *Pipe) Recv() (chirp.Packet, error) {
	c.init() // start service goroutine, if needed

	// When sharing a pipe with multiple descendants, e.g., when the child
	// process is a shell that will execute other subprocesses that can access
	// the pipe, the shell may not (in general) close the write end of its dup
	// of the pipe. This means a child may not get EOF from the reader after
	// closing its write half, since there is another dup still active.
	//
	// Calling [os.File.Close] does not suffice, as the polling shims can defer
	// closing the actual file descriptor. Moreover, even if we close the
	// descriptor explicitly, that may not unblock a poll. To avoid blocking
	// indefinitely in that case, perform the read in a goroutine and give up if
	// the channel closes before we get an answer.
	select {
	case <-c.ctx.Done():
		// closed
	case c.req <- struct{}{}:
		select {
		case <-c.ctx.Done():
			// closed
		case r := <-c.rsp:
			return r.pkt, r.err
		}
	}
	return chirp.Packet{}, net.ErrClosed
}

// Close implements part of the [chirp.Channel] interface.
func (c *Pipe) Close() error {
	c.cancel()
	<-c.ctx.Done()
	return c.ch.Close()
}

// init lazily initializes the service goroutine on the first attempt to
// receive from c, so that we do not run a goroutine for a channel that may be
// abandoned before first use.
func (c *Pipe) init() {
	c.setup.Do(func() {
		req := make(chan struct{})
		rsp := make(chan recv)
		go func() {
			for {
				select {
				case <-c.ctx.Done():
					return
				case <-req:
					// received request, perform a read
				}

				pkt, err := c.ch.Recv()
				select {
				case <-c.ctx.Done():
					return
				case rsp <- recv{pkt: pkt, err: err}:
					// sent response
				}
			}
		}()
		c.req = req
		c.rsp = rsp
	})
}
