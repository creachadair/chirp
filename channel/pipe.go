// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

package channel

import (
	"context"
	"net"
	"os"
	"time"

	"github.com/creachadair/chirp"
)

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
	r, w   *os.File
	ch     IOChannel
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
	// We cannot "cancel" a read, but we can set its deadline in the past, which
	// will interrupt a pending read for a pipe.
	stop := context.AfterFunc(c.ctx, func() {
		c.r.SetReadDeadline(time.Unix(0, 0)) // long ago
	})
	pkt, err := c.ch.Recv()

	// Cancel the cancellation, if it has not already occurred. Since the
	// deadline will not fire unless c.ctx is expired, we do not have to worry
	// about resetting the deadline; if it happens, the channel is closed.
	//
	// We may already have a valid packet, though, so only report closed if it
	// affected the Recv call (if it didn't, the next call will hit the close).
	stop()
	if err != nil && c.ctx.Err() != nil {
		return chirp.Packet{}, net.ErrClosed
	}
	return pkt, err
}

// Close implements part of the [chirp.Channel] interface.
func (c *PipeChannel) Close() error {
	c.cancel()
	<-c.ctx.Done()
	return c.ch.Close()
}
