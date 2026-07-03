// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

package channel_test

import (
	"errors"
	"net"
	"os"
	"testing"

	"github.com/creachadair/chirp"
	"github.com/creachadair/chirp/channel"
	"github.com/creachadair/taskgroup"
	"github.com/google/go-cmp/cmp"
)

func runPingPong(t *testing.T, s, c chirp.Channel, want chirp.Packet) {
	t.Helper()
	echo := taskgroup.Run(func() {
		pkt, err := s.Recv()
		if err != nil {
			t.Errorf("B Recv: %v", err)
		}
		if err := s.Send(pkt); err != nil {
			t.Errorf("B Send: %v", err)
		}
	})
	if err := c.Send(want); err != nil {
		t.Errorf("A Send: %v", err)
	}
	got, err := c.Recv()
	if err != nil {
		t.Errorf("A Recv: %v", err)
	}
	if diff := cmp.Diff(got, want); diff != "" {
		t.Errorf("Returned packet (-got, +want):\n%s", diff)
	}
	echo.Wait()
}

func checkNoTraffic(t *testing.T, ch chirp.Channel) {
	if err := ch.Send(chirp.Packet{}); !errors.Is(err, net.ErrClosed) {
		t.Errorf("c.Send: got %v, want %v", err, net.ErrClosed)
	}
	if pkt, err := ch.Recv(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("c.Recv: got %+v, %v; want %v", pkt, err, net.ErrClosed)
	}
}

func TestDirect(t *testing.T) {
	s, c := channel.Direct()

	runPingPong(t, s, c, chirp.Packet{Type: 101, Payload: []byte("hello, world")})

	if err := s.Close(); err != nil {
		t.Errorf("s.Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("c.Close: %v", err)
	}

	checkNoTraffic(t, s)
	checkNoTraffic(t, c)
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

func TestPipe(t *testing.T) {
	s, c, err := channel.NewPipe()
	if err != nil {
		t.Fatalf("Pipe failed: %v", err)
	}
	runPingPong(t, s, c, chirp.Packet{Type: 123, Payload: []byte("four five")})

	if err := s.Close(); err != nil {
		t.Errorf("s.Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("c.Close: %v", err)
	}

	checkNoTraffic(t, s)
	checkNoTraffic(t, c)
}

func TestConnectPipe(t *testing.T) {
	sr, cw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}
	cr, sw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}

	s := channel.ConnectPipe(sr, sw)
	c := channel.ConnectPipe(cr, cw)
	runPingPong(t, s, c, chirp.Packet{Type: 223, Payload: []byte("wharrgarbl")})

	if err := s.Close(); err != nil {
		t.Errorf("s.Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("c.Close: %v", err)
	}

	checkNoTraffic(t, s)
	checkNoTraffic(t, c)
}

type noConnStub struct{ chirp.Channel }

type netConnStub struct {
	chirp.Channel // placeholder to satisfy the interface
	conn          net.Conn
}

func (n netConnStub) NetConn() net.Conn { return n.conn }
