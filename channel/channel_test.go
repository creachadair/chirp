// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

package channel_test

import (
	"net"
	"testing"

	"github.com/creachadair/chirp"
	"github.com/creachadair/chirp/channel"
	"github.com/creachadair/taskgroup"
)

func TestDirect(t *testing.T) {
	c, s := channel.Direct()

	g := taskgroup.New(nil)
	g.Go(func() error {
		pkt := new(chirp.Packet)
		if err := c.Send(pkt); err != nil {
			t.Errorf("A Send: %v", err)
		}
		got, err := c.Recv()
		if err != nil {
			t.Errorf("A Recv: %v", err)
		}
		if got != pkt {
			t.Errorf("Packet: got %v, want %v", got, pkt)
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

	if err := c.Close(); err != nil {
		t.Errorf("c.Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("s.Close: %v", err)
	}

	if err := c.Send(nil); err == nil {
		t.Error("c.Send after close did not report an error")
	}
	if err := s.Send(nil); err == nil {
		t.Error("s.Send after close did not report an error")
	}
	if pkt, err := c.Recv(); err == nil {
		t.Errorf("c.Recv after close: got %+v", pkt)
	} else {
		t.Logf("Error OK: %v", err)
	}
	if pkt, err := s.Recv(); err == nil {
		t.Errorf("s.Recv after close: got %+v", pkt)
	} else {
		t.Logf("Error OK: %v", err)
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
