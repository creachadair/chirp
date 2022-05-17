// Package peers provides support code for managing and testing peers.
package peers

import (
	"github.com/creachadair/chirp"
	"github.com/creachadair/chirp/channel"
)

// Local is a pair of in-memory connected peers, suitable for testing.
type Local struct {
	A *chirp.Peer
	B *chirp.Peer
}

// Stop shuts down both the peers and blocks until both have exited.
func (p *Local) Stop() error {
	aerr := p.A.Stop()
	berr := p.B.Stop()
	if aerr != nil {
		return aerr
	}
	return berr
}

// NewLocal creates a pair of in-memory connected peers, that communicate via a
// direct channel without encoding.
func NewLocal() *Local {
	a2b, b2a := channel.Direct()
	return &Local{
		A: new(chirp.Peer).Start(a2b),
		B: new(chirp.Peer).Start(b2a),
	}
}
