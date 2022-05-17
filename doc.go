// Packet chirp implements the Chirp v0 protocol.
//
// Chirp is a lightweight remote procedure call protocol. Peers exchange binary
// packets over a shared reliable channel.  The protocol is intended to be easy
// to implement in a variety of different languages and to perform efficiently
// over stream-oriented local transport mechanisms such as sockets and pipes.
// The packet format is byte-oriented and uses fixed header formats to minimize
// the amount of bit-level manipulation necessary to encode and decode packets.
//
// Channels
//
// The Channel interface defines the ability to send and receive packets as
// defined by the Chirp specification. A Channel implementation must allow
// concurrent use by one sender and one receiver.
//
// The channel package provides some basic implementations of this interface.
//
// Peers
//
// The core type defined by this package is the Peer. Peers can simultaneously
// initiate and and service calls with another peer over a Channel.
//
// Calls
//
// A call is a request-response exchange between two peers, consisting of a
// request and a corresponding response.  This is the primary communication
// between peers.  The peer that initiates the call is the caller, the peer
// that responds is the callee. Calls may propagate in either direction.
package chirp
