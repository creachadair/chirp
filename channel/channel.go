// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

// Package channel provides implementations of the chirp.Channel interface.
package channel

import (
	"bufio"
	"io"
	"net"

	"github.com/creachadair/chirp"
)

// Direct constructs a connected pair of in-memory channels that pass packets
// directly without encoding into binary. Packets sent to A are received by B
// and vice versa.
func Direct() (A, B chirp.Channel) {
	a2b := make(chan chirp.Packet)
	b2a := make(chan chirp.Packet)
	A = direct{a2b: a2b, b2a: b2a}
	B = direct{a2b: b2a, b2a: a2b}
	return
}

type direct struct {
	a2b chan<- chirp.Packet
	b2a <-chan chirp.Packet
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
func (d direct) Close() (err error) {
	defer safeClose(&err)
	close(d.a2b)
	return nil
}

func safeClose(err *error) {
	if x := recover(); x != nil && *err == nil {
		*err = net.ErrClosed
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
