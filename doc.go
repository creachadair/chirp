// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

// Package chirp implements the [Chirp v0] protocol.
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
// initiate and service calls with another peer over a [Channel].
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
// A call is an exchange between two peers, consisting of a request and a
// corresponding response.  This is the primary communication between peers.
// The peer that initiates the call is the caller, the peer
// that responds is the callee. Calls may propagate in either direction.
//
// To define method handlers for inbound calls on the peer, use the
// [Peer.Handle] method to register a handler for a method name:
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
// # Callbacks
//
// A method handler may "call back" to methods of the remote peer. To do so,
// the handler uses [ContextPeer] to obtain the local peer, and executes its
// [Peer.Call] method. This behaves as any other call made by the local peer:
//
//	func handle(ctx context.Context, req *chirp.Request) ([]byte, error) {
//	    rsp, err := chirp.ContextPeer(ctx).Call(ctx, "hello", []byte("world"))
//	    if err != nil {
//	       return nil, err
//	    }
//	    return doStuffWith(rsp, req.Data)
//	}
//
// # Local Calls
//
// To invoke a handler directly on the local peer, use [Peer.Exec]:
//
//	rsp, err := p.Exec(ctx, "do-a-thing", []byte("some data"))
//	if err != nil {
//	   log.Fatalf("Exec failed: %v", err)
//	}
//
// Exec does not send any packets to the remote peer. If the method handler
// invokes [Peer.Call], that call also invokes its target locally. Errors
// reported by p.Exec have concrete type [*chirp.CallError].
//
// # Custom Packet Handlers
//
// To handle packet types other than [Request], [Response], and [Cancel], the
// caller can use the [Peer.SendPacket] and [Peer.HandlePacket] methods.
// SendPacket allows the caller to send an arbitrary packet to the peer. Peers
// that do not understand a packet type will silently discard it (per the
// spec).
//
// HandlePacket registers a callback that will be invoked when a packet is
// received matching the specified type. If the callback reports an error or
// panics, it is treated as protocol fatal.
//
// # Metrics
//
// Peers maintain a collection of metrics while running. Use the [Peer.Metrics]
// method to obtain an [expvar.Map] containing the metrics exported by the
// peer. By default, metrics are shared globally among all peers.
//
// The metrics currently exported by peers include:
//
//   - packets_received: counter of packets received
//   - packets_sent: counter of packets sent
//   - packets_dropped: counter of packets received and discarded
//   - calls_in: counter of inbound call requests received
//   - calls_in_failed: counter of inbound call requests resulting in errors
//   - calls_active: gauge of inbound calls currently active
//   - calls_out: counter of outbound call requests sent
//   - calls_out_failed: counter of outbound call requests resulting in errors
//   - cancels_in: counter of cancellation requests received
//   - calls_pending: gauge of outbound calls currently pending
//
// Additional metrics may be added in the future. It is safe for the caller to
// modify the metrics map to add, update, and remove entries.
//
// Using [Peer.Detach] "detaches" the metrics for a peer, and thereafter the
// metrics for that peer and its (recursive) clones will not affect the global
// metrics, nor the metrics of the peer from which they were detached.
//
//	p := chirp.NewPeer()  // p updates global metrics
//	p.Detach()            // now p has its own metrics
//	cp := p.Clone()       // cp updates the same metrics as p
//	ccp := cp.Clone()     // ccp updates the same metrics as cp and p
//
// [Chirp v0]: https://github.com/creachadair/chirp/blob/main/spec.md
package chirp
