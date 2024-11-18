// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

// Package chirp implements the Chirp v0 protocol.
//
// Chirp is a lightweight remote procedure call protocol. Peers exchange binary
// packets over a shared reliable channel.  The protocol is intended to be easy
// to implement in a variety of different languages and to perform efficiently
// over stream-oriented local transport mechanisms such as sockets and pipes.
// It uses a byte-oriented packet format with fixed headers to minimize the
// amount of bit-level manipulation necessary to encode and decode messages.
//
// # Peers
//
// The core type defined by this package is the [Peer]. Peers concurrently
// initiate and and service calls with another peer over a [Channel].
//
// To create a new, unstarted peer:
//
//	p := chirp.NewPeer()
//
// To start the service routine, call the Start method with a channel connected
// to another peer:
//
//	p.Start(ch)
//
// The peer runs until [Peer.Stop] is called, the channel is closed by the
// remote peer, or a protocol fatal error occurs. Call [Peer.Wait] to wait for
// the peer to exit an return its status:
//
//	if err := p.Wait(); err != nil {
//	   log.Fatalf("Peer failed: %v", err)
//	}
//
// # Channels
//
// The [Channel] interface defines the ability to send and receive packets as
// defined by the Chirp specification. A Channel implementation must allow
// concurrent use by one sender and one receiver.
//
// The channel package provides some basic implementations of this interface.
//
// # Calls
//
// A call is a request-response exchange between two peers, consisting of a
// request and a corresponding response.  This is the primary communication
// between peers.  The peer that initiates the call is the caller, the peer
// that responds is the callee. Calls may propagate in either direction.
//
// To define method handlers for inbound calls on the peer, use the
// [Peer.Handle] method to register a handler for the method ID:
//
//	func echo(ctx context.Context, req *chirp.Request) ([]byte, error) {
//	   return req.Data, nil
//	}
//
//	p.Handle("do-a-thing", echo)
//
// To issue a call to the remote peer, use the [Peer.Call] method:
//
//	rsp, err := p.Call(ctx, "do-a-thing", []byte("some data"))
//	if err != nil {
//	   log.Fatalf("Call failed: %v", err)
//	}
//
// Errors returned by p.Call have concrete type [*chirp.CallError].
//
// To call a method directly on the local peer, use the [Peer.Exec] method:
//
//	rsp, err := p.Exec(ctx, "do-a-thing", []byte("some data"))
//	if err != nil {
//	   log.Fatalf("Exec failed: %v", err)
//	}
//
// Exec does not send any packets to the remote peer unless the method handler
// does so internally.
//
// # Custom Packet Handlers
//
// To handle packet types other than [Request], [Response], and [Cancel], the
// caller can use the [Peer.SendPacket] and [Peer.HandlePacket] methods
// SendPacket allows the caller to send an arbitrary packet to the peer. Peers
// that do not understand a packet type will silently discard it (per the
// spec).
//
// HandlePacket registers a callback that will be invoked when a packet is
// received matching the specified type. If the callback reports an error or
// panics, it is treated as protocol fatal.
package chirp
