// Copyright (C) 2023 Michael J. Fromberger. All Rights Reserved.

// Package catalog defines a mapping from mnemonic string names to method IDs
// for use with a chirp.Peer.  Method names are not exchanged between peers on
// the wire, but a Catalog can be encoded by a method handler and sent from one
// peer to another in a request.
//
// # Usage
//
// Construct a new empty catalog and add methods to it:
//
//	cat := catalog.New().Add("foo", "bar", "baz")
//
// Add assigns method IDs to the specified names. To recover the assigned ID
// use the Lookup method:
//
//	id := cat.Lookup("foo")
//
// If you want to choose the ID, use Set:
//
//	cat.Set("quux", 125)
//
// Method IDs are assigned systematically, so that repeating the same sequence
// of Add and Set calls will always result in the same method IDs.
//
// To associate a catalog with a specific peer, use Bind. This creates a copy
// of the catalog sharing the same methods but a (possibly) different peer:
//
//	cat2 := cat.Bind(p)
//
// On a peer that implements these methods, use Handle:
//
//	cat.Bind(peer1).
//	  Handle("foo", handleFoo).
//	  Handle("bar", handleBar)
//
// Note that Handle will panic if given a name not registered with the catalog.
//
// On a peer that wants to call these methods, use Call:
//
//	rsp, err := cat.Bind(peer2).Call(ctx, "foo", data)
//
// A Catalog provides a Handler method that can be bound to a peer to send the
// catalog as a request payload:
//
//	// Add a method that serves the catalog.
//	cat.Set("catalog", 1)
//
//	// Bind the catalog method to a peer.
//	cat.Bind(peer1).Handle("catalog", cat.Handler)
//
//	// Call the catalog from another peer.
//	rsp, err := cat.Bind(peer2).Call(ctx, "catalog", nil)
package catalog

import (
	"context"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/creachadair/chirp"
)

// A Catalog associates a peer with a static mapping from method names to IDs
// for use with that peer.
type Catalog struct {
	peer    *chirp.Peer
	methods map[string]uint32
}

// New creates a new empty, unbound catalog to map names to method IDs.  It is
// safe to copy the resulting value, all copies share a reference to the same
// name to ID mapping.
func New() Catalog { return Catalog{methods: make(map[string]uint32)} }

// Add adds the specified names to c with fresh positive IDs, and returns c to
// allow chaining.
func (c Catalog) Add(names ...string) Catalog {
	for _, name := range names {
		c.Set(name, c.pickUnusedID())
	}
	return c
}

// Set maps name to methodID in c, and return c to allow chaining.  If name was
// already mapped in c, the existing mapping is replaced.
//
// The name mapping of a catalog is shared among all copies of it.  It is not
// safe to call Set while c is used concurrently by other goroutines without
// external synchronization.
func (c Catalog) Set(name string, methodID uint32) Catalog {
	c.methods[name] = methodID
	return c
}

func (c Catalog) pickUnusedID() uint32 {
	var max uint32
	for _, id := range c.methods {
		if id > max {
			max = id
		}
	}
	return max + 1
}

// Bind returns a copy of c bound to the specified peer.
func (c Catalog) Bind(peer *chirp.Peer) Catalog { return Catalog{peer: peer, methods: c.methods} }

// Peer returns the peer associated with c, or nil if the c is unbound.
func (c Catalog) Peer() *chirp.Peer { return c.peer }

// Lookup returns the method ID assigned to name, or 0.
//
// Note that the caller may Set a method with ID 0, but assigned IDs will
// always be positive, so a 0 return value of 0 means name was not assigned an
// ID even if it is a valid mapping for the catalog.
func (c Catalog) Lookup(name string) uint32 { return c.methods[name] }

// Call calls the method bound to name on the remote peer.
// If name is not known in the catalog, Call uses method ID 0.
// Call will panic if c is not bound to a peer.
func (c Catalog) Call(ctx context.Context, name string, data []byte) (*chirp.Response, error) {
	return c.peer.Call(ctx, c.methods[name], data)
}

// Exec calls the method bound to name on the local peer.
// If name is not known in the catalog, Exec reports an error.
// Exec will panic if c is not bound to a peer.
func (c Catalog) Exec(ctx context.Context, name string, req *chirp.Request) ([]byte, error) {
	return c.peer.Exec(ctx, c.methods[name], req)
}

// Handle binds the specified method to the peer associated with c,
// and returns c to permit chaining.
// Handle will panic if c is not bound to a peer, or if name is not a method
// name known by the catalog.
func (c Catalog) Handle(name string, handler chirp.Handler) Catalog {
	methodID, ok := c.methods[name]
	if !ok {
		panic(fmt.Sprintf("method %q not known", name))
	}
	c.peer.Handle(methodID, handler)
	return c
}

// Encode encodes c in binary format.
//
// THe wire format of the catalog comprises the names of all defined methods in
// lexicgraphic order, followed by the corresponding method IDs in the reverse
// order of the names.
//
// Each name is encoded as a big-endian uint16 length followed by that many
// bytes of the name. Each method ID is encoded as a big-endian uint32.
func (c Catalog) Encode() []byte {
	if len(c.methods) == 0 {
		return nil
	}
	var nlen int
	names := make([]string, 0, len(c.methods))
	for name := range c.methods {
		names = append(names, name)
		nlen += 2 + len(name) // +2 for length tag
	}
	sort.Strings(names)
	buf := make([]byte, nlen+4*len(c.methods))
	npos, mpos := 0, len(buf)
	putName := func(s string) {
		binary.BigEndian.PutUint16(buf[npos:], uint16(len(s)))
		npos += 2
		npos += copy(buf[npos:], s)
	}
	putMethod := func(id uint32) {
		mpos -= 4
		binary.BigEndian.PutUint32(buf[mpos:], id)
	}

	for _, name := range names {
		putName(name)
		putMethod(c.methods[name])
	}
	return buf
}

// Decode decodes data as a Catalog payload.
func (c *Catalog) Decode(data []byte) error {
	if c.methods == nil {
		c.methods = make(map[string]uint32)
	} else {
		clear(c.methods)
	}
	npos, mpos := 0, len(data)
	for {
		if npos+2 > len(data) || npos > mpos {
			return fmt.Errorf("truncated catalog at offset %d", npos)
		} else if npos == mpos {
			break
		}

		nlen := int(binary.BigEndian.Uint16(data[npos:]))
		npos += 2
		if npos+nlen > len(data) {
			return fmt.Errorf("truncated name at offset %d", npos)
		}

		mpos -= 4
		if mpos < npos+nlen {
			return fmt.Errorf("truncated ID at offset %d", mpos)
		}
		id := binary.BigEndian.Uint32(data[mpos:])

		c.methods[string(data[npos:npos+nlen])] = id
		npos += nlen
	}
	return nil
}

// Handler is a Handler that reports the contents of the catalog.
func (c Catalog) Handler(_ context.Context, req *chirp.Request) ([]byte, error) {
	return c.Encode(), nil
}
