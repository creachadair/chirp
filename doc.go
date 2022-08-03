// Packet chirp implements the Chirp v0 protocol.
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
