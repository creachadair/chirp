// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

// Package channel provides implementations of the chirp.Channel interface.
package channel

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"sync"

	"github.com/creachadair/chirp"
)

// Direct constructs a connected pair of in-memory channels that pass packets
// directly without encoding into binary. Packets sent to A are received by B
// and vice versa.
func Direct() (A, B chirp.Channel) {
	a2b := make(chan chirp.Packet)
	b2a := make(chan chirp.Packet)
	A = direct{a2b: a2b, b2a: b2a, stop: func() { defer close(b2a) }}
	B = direct{a2b: b2a, b2a: a2b, stop: func() { defer close(a2b) }}
	return
}

type direct struct {
	a2b  chan<- chirp.Packet
	b2a  <-chan chirp.Packet
	stop func()
}

// Send implements a method of the [chirp.Channel] interface.
func (d direct) Send(pkt chirp.Packet) (err error) {
	defer safeClose(&err)
	d.a2b <- pkt
	return nil
}

// Recv implements a method of the [chirp.Channel] interface.
func (d direct) Recv() (chirp.Packet, error) {
	pkt, ok := <-d.b2a
	if !ok {
		return pkt, net.ErrClosed
	}
	return pkt, nil
}

// Close implements a method of the [chirp.Channel] interface.
// This implementation never reports an error.
func (d direct) Close() error {
	defer safeClose(nil)
	close(d.a2b)
	d.stop() // inform the other side
	return nil
}

func safeClose(errp *error) {
	if x := recover(); x != nil && errp != nil && *errp == nil {
		*errp = net.ErrClosed
	}
}

// IO constructs a channel that receives from r and sends to wc.
func IO(r io.Reader, wc io.WriteCloser) IOChannel {
	// N.B. The bufio package will reuse existing buffers if possible.
	return IOChannel{r: bufio.NewReader(r), w: bufio.NewWriter(wc), c: wc}
}

// An IOChannel sends and receives packets on a reader and a writer.
type IOChannel struct {
	r *bufio.Reader
	w *bufio.Writer
	c io.Closer
}

// Send implements a method of the [chirp.Channel] interface.
func (c IOChannel) Send(pkt chirp.Packet) error {
	if _, err := pkt.WriteTo(c.w); err != nil {
		return err
	}
	return c.w.Flush()
}

// Recv implements a method of the [chirp.Channel] interface.
func (c IOChannel) Recv() (chirp.Packet, error) {
	var pkt chirp.Packet
	_, err := pkt.ReadFrom(c.r)
	return pkt, err
}

// Close implements a method of the [chirp.Channel] interface.
func (c IOChannel) Close() error { return c.c.Close() }

// NetConn implements the optional [NetConner] interface.  It returns a non-nil
// value if c was constructed with a [net.Conn] as its writer.
func (c IOChannel) NetConn() net.Conn {
	if nc, ok := c.c.(net.Conn); ok {
		return nc
	}
	return nil
}

// NetConn returns the [net.Conn] impementation underlying c, if one is
// available, or else nil.  If c implements [NetConner] its corresponding
// method will be called to locate a connection.  If c == nil, NetConn reports
// nil.
//
// The [chirp.Channel] retains ownership of any [net.Conn] that is returned.
// Note that reading, writing or otherwise changing the state of the resulting
// connection may invalidate assumptions of the channel that uses it.
func NetConn(c chirp.Channel) net.Conn {
	if nc, ok := c.(NetConner); ok {
		return nc.NetConn()
	}
	return nil
}

// NetConner is an optional interface that a [chirp.Channel] may implement to
// expose an underlying [net.Conn] used by its implementation. The method
// should report nil if its receiver does not have a [net.Conn].
type NetConner interface {
	NetConn() net.Conn
}

// Pipe establishes a connected pair of [os.Pipe] for a pair of peers,
// and returns [PipeChannel] values wrapping them.
// Sends to A are received by B and vice versa.
// Each call to Pipe returns a distinct pair of pipes.
func Pipe() (A, B *PipeChannel, _ error) {
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

// ConnectPipe constructs a new [PipeChannel] wrapping the specified files.
func ConnectPipe(r, w *os.File) *PipeChannel {
	ctx, cancel := context.WithCancel(context.Background()) // cancel called by Close
	return &PipeChannel{
		ctx:    ctx,
		cancel: cancel,
		r:      r,
		w:      w,
		ch:     IO(r, w),
	}
}

// A PipeChannel implements the [chirp.Channel] interface around an [os.Pipe].
type PipeChannel struct {
	ctx    context.Context
	cancel context.CancelFunc
	setup  sync.Once
	r, w   *os.File
	ch     IOChannel

	req chan<- struct{}
	rsp <-chan recv
}

// Files returns the underlying [os.File] values used by c.
func (c *PipeChannel) Files() (r, w *os.File) { return c.r, c.w }

// Send implements part of the [chirp.Channel] interface.
func (c *PipeChannel) Send(pkt chirp.Packet) error {
	if c.ctx.Err() != nil {
		return net.ErrClosed
	}
	return c.ch.Send(pkt)
}

// Recv implements part of the [chirp.Channel] interface.
func (c *PipeChannel) Recv() (chirp.Packet, error) {
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
func (c *PipeChannel) Close() error {
	c.cancel()
	<-c.ctx.Done()
	return c.ch.Close()
}

// init lazily initializes the service goroutine on the first attempt to
// receive from c, so that we do not run a goroutine for a channel that may be
// abandoned before first use.
func (c *PipeChannel) init() {
	c.setup.Do(func() {
		req := make(chan struct{})
		rsp := make(chan recv)
		go func() {
			for {
				select {
				case <-c.ctx.Done():
					return
				case <-req:
					pkt, err := c.ch.Recv()
					select {
					case rsp <- recv{pkt: pkt, err: err}:
						// sent to Recv
					case <-c.ctx.Done():
						return
					}
				}
			}
		}()
		c.req = req
		c.rsp = rsp
	})
}
