// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

package channel_test

import (
	"errors"
	"net"
	"testing"

	"github.com/creachadair/chirp"
	"github.com/creachadair/chirp/channel"
	"github.com/creachadair/taskgroup"
	"github.com/google/go-cmp/cmp"
)

func TestDirect(t *testing.T) {
	c, s := channel.Direct()

	g := taskgroup.New(nil)
	g.Go(func() error {
		var pkt chirp.Packet
		if err := c.Send(pkt); err != nil {
			t.Errorf("A Send: %v", err)
		}
		got, err := c.Recv()
		if err != nil {
			t.Errorf("A Recv: %v", err)
		}
		if diff := cmp.Diff(got, pkt); diff != "" {
			t.Errorf("Returned packet (-got, +want):\n%s", diff)
		}
		return nil
	})
	g.Go(func() error {
		pkt, err := s.Recv()
		if err != nil {
			t.Errorf("B Recv: %v", err)
		}
		if err := s.Send(pkt); err != nil {
			t.Errorf("B Send: %v", err)
		}
		return nil
	})
	g.Wait()

	// Close the client side, which should succeed.
	if err := c.Close(); err != nil {
		t.Errorf("c.Close: %v", err)
	}

	// A Send from either side should now fail.
	if err := c.Send(chirp.Packet{}); !errors.Is(err, net.ErrClosed) {
		t.Errorf("c.Send after c.Close: got %v, want %v", err, net.ErrClosed)
	}
	if err := s.Send(chirp.Packet{}); !errors.Is(err, net.ErrClosed) {
		t.Errorf("s.Send after c.Close: got %v, want %v", err, net.ErrClosed)
	}

	// A Recv from either side should also fail.
	if pkt, err := c.Recv(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("c.Recv after close: got %+v, %v; want %v", pkt, err, net.ErrClosed)
	}

	if pkt, err := s.Recv(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("s.Recv after close: got %+v, %v; want %v", pkt, err, net.ErrClosed)
	}

	// Close the server side, which should succeed.
	if err := s.Close(); err != nil {
		t.Errorf("s.Close: %v", err)
	}
}

func TestNetConn(t *testing.T) {
	dirA, dirB := channel.Direct()
	pa, _ := net.Pipe()
	io := channel.IO(net.Pipe())
	t.Cleanup(func() { io.Close() })

	tests := []struct {
		input   chirp.Channel
		hasConn bool
	}{
		{nil, false}, // but should not panic

		// The Direct type from this library.
		{dirA, false},
		{dirB, false},

		// The IOChannel type from this library.
		{io, true},

		// An arbitrary type implementing channel.NetConner.
		{netConnStub{}, false},
		{netConnStub{conn: pa}, true},

		// An arbitrary type not implementing channel.NetConner.
		{noConnStub{}, false},
	}
	for _, tc := range tests {
		got := channel.NetConn(tc.input)
		if (got != nil) != tc.hasConn {
			t.Errorf("NetConn(%+v): has conn %v, want %v", tc.input, got != nil, tc.hasConn)
		}
	}
}

type noConnStub struct{ chirp.Channel }

type netConnStub struct {
	chirp.Channel // placeholder to satisfy the interface
	conn          net.Conn
}

func (n netConnStub) NetConn() net.Conn { return n.conn }
