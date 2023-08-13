// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

package channel_test

import (
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
