// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

// Package chirp implements the Chirp v0 protocol.
//
// Chirp is a lightweight remote procedure call protocol. Peers exchange binary
// packets over a shared reliable channel.  The protocol is intended to be easy
// to implement in a variety of different languages and to perform efficiently
// over stream-oriented local transport mechanisms such as sockets and pipes.
// The packet format is byte-oriented and uses fixed header formats to minimize
// the amount of bit-level manipulation necessary to encode and decode packets.
//
// # Peers
//
// The core type defined by this package is the Peer. Peers can simultaneously
// initiate and and service calls with another peer over a Channel.
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
// The peer runs until its Stop method is called, the channel is closed by the
// remote peer, or a protocol fatal error occurs. Call Wait to wait for the
// peer to exit an return its status:
//
//	if err := p.Wait(); err != nil {
//	   log.Fatalf("Peer failed: %v", err)
//	}
//
// # Channels
//
// The Channel interface defines the ability to send and receive packets as
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
// To define method handlers for inbound calls on the peer, use the Handle
// method to register a handler for the method ID:
//
//	func echo(ctx context.Context, req *chirp.Request) ([]byte, error) {
//	   return req.Data, nil
//	}
//
//	p.Handle(100, echo)
//
// To issue a call to the remote peer, use the Call method:
//
//	rsp, err := p.Call(ctx, 100, []byte("some data"))
//	if err != nil {
//	   log.Fatalf("Call failed: %v", err)
//	}
//
// Errors returned by p.Call have concrete type *chirp.CallError.
//
// # Custom Packet Handlers
//
// To handle packet types other than Request, Response, and Cancel, the caller
// can use the SendPacket and HandlePacket methods of the Peer. SendPacket
// allows the caller to send an arbitrary packet to the peer. Peers that do not
// understand a packet type will silently discard it (per the spec).
//
// HandlePacket registers a callback that will be invoked when a packet is
// received matching the specified type. If the callback reports an error or
// panics, it is treated as protocol fatal.
package chirp

import (
	"context"
	"fmt"
	"maps"
)

// A Catalog associates a peer with a static mapping from method names to IDs
// for use with that peer. A Catalog can be used to give mnemonic names to
// methods to share across packages.  Method names are not exchanged between
// peers on the wire.
type Catalog struct {
	peer    *Peer
	methods map[string]uint32
}

// NewCatalog creates a new unbound catalog with the given method name to ID
// mapping. The keys of the map define which methods a peer can bind or call.
// The input map is copied, and subsequent modifications of the input map do
// not affect the catalog.
func NewCatalog(methods map[string]uint32) Catalog {
	return Catalog{methods: maps.Clone(methods)}
}

// Set maps name to methodID in c, and return c to allow chaining.  If name was
// already mapped in c, the existing mapping is replaced. The name mapping of a
// catalog is shared among all the copies derived from it.
//
// It is not safe to call Set while c is used concurrently by other goroutines
// without external synchronization.
func (c Catalog) Set(name string, methodID uint32) Catalog {
	c.methods[name] = methodID
	return c
}

// Bind returns a copy of c bound to the specified peer.
func (c Catalog) Bind(peer *Peer) Catalog { return Catalog{peer: peer, methods: c.methods} }

// Peer returns the peer associated with c, or nil if the c is unbound.
func (c Catalog) Peer() *Peer { return c.peer }

// Call calls the method bound to name on the remote peer.
// If name is not known in the catalog, Call uses method ID 0.
// Call will panic if c is not bound to a peer.
func (c Catalog) Call(ctx context.Context, name string, data []byte) (*Response, error) {
	return c.peer.Call(ctx, c.methods[name], data)
}

// Exec calls the method bound to name on the local peer.
// If name is not known in the catalog, Exec reports an error.
// Exec will panic if c is not bound to a peer.
func (c Catalog) Exec(ctx context.Context, name string, req *Request) ([]byte, error) {
	return c.peer.Exec(ctx, c.methods[name], req)
}

// Handle binds the specified method to the peer associated with c,
// and returns c to permit chaining.
// Handle will panic if c is not bound to a peer, or if name is not a method
// name known by the catalog.
func (c Catalog) Handle(name string, handler Handler) Catalog {
	methodID, ok := c.methods[name]
	if !ok {
		panic(fmt.Sprintf("method %q not known", name))
	}
	c.peer.Handle(methodID, handler)
	return c
}
