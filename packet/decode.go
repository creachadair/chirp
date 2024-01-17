// Copyright (C) 2024 Michael J. Fromberger. All Rights Reserved.

package packet

import "fmt"

// A Decoder is a value that supports being decoded from binary form.
type Decoder interface {
	// Decode decodes into the receiver from a prefix of buf, and returns the
	// number of bytes consumed. If there is no valid encoding at the front of
	// buf, Decode returns -1.
	Decode(buf []byte) int
}

// Decode implements the Decoder interface.
func (v *Vint30) Decode(buf []byte) int {
	nb, z := ParseVint30(buf)
	if nb < 0 {
		return -1
	}
	*v = z
	return nb
}

// Decode implements the Decoder interface.
func (b *Bytes) Decode(buf []byte) int {
	nb, z := ParseBytes(buf)
	if nb < 0 {
		return -1
	}
	*b = z
	return nb
}

// Decode implements the Decoder interface.
func (s String) Decode(buf []byte) int {
	nb, _ := ParseLiteral(string(s), buf)
	return nb
}

// Decode implements the Decoder interface.
func (b *Bool) Decode(buf []byte) int {
	nb, v := ParseBool(buf)
	if nb < 0 {
		return -1
	}
	*b = v
	return nb
}

// Decode implements the Decoder interface.  Decoding succeeds if buf is at
// least as long as *r, and in that case copies those bytes into r.
func (r *Raw) Decode(buf []byte) int {
	nb, v := ParseRaw(len(*r), buf)
	if nb < 0 {
		return -1
	}
	copy(*r, v)
	return nb
}

// Parse parses buf into the specified decoder values, returning the total
// number of bytes consumed.
func Parse(buf []byte, into ...Decoder) (int, error) {
	var nr int
	cur := buf
	for i, dec := range into {
		nb := dec.Decode(cur)
		if nb < 0 {
			return nr, fmt.Errorf("arg %d: invalid %T", i+1, dec)
		}
		nr += nb
		cur = cur[nb:]
	}
	return nr, nil
}
