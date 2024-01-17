// Copyright (C) 2024 Michael J. Fromberger. All Rights Reserved.

// Package packet provides support for encoding and decoding binary packet data.
package packet

// Vint30 is an unsigned 30-bit integer that uses a variable-width encoding
// from 1 to 4 bytes.
//
//   - Values v < 64 are encoded as 1 byte.
//   - Values 64 ≤ v < 16384 are encoded as 2 bytes.
//   - Values 16384 ≤ v < 4194304 are encoded as 3 bytes.
//   - Values 4194304 ≤ v < 1073741824 are encoded as 4 bytes.
//
// Values v ≥ 1073741824 cannot be represented by a Vint30.
//
// A value is encoded as a 32-bit value in little-endian order, with the excess
// length packed into the lowest-order 2 bits of the overall value. The first
// byte of the encoded form has the 6 lowest order bits plus the tag:
//
//	 _ ... _ _ _ _ _ _ d d < number of additional bytes
//	31 ... 7 6 5 4 3 2 1 0
//	^^^^^^^^^^^^^^^^^^
//	  30-bit value
//
// This makes the encoding self-framing, as the decoder can read the first byte
// to discover the length of the full encoding.
type Vint30 uint32

// MaxVint30 is the maximum value that can be encoded by a Vint30.
const MaxVint30 = 1<<30 - 1

// EncodedLen reports the number of bytes needed to encode v.
// If v is too large to be represented, EncodedLen returns -1.
func (v Vint30) EncodedLen() int {
	switch {
	case v < (1 << 6):
		return 1
	case v < (1 << 14):
		return 2
	case v < (1 << 22):
		return 3
	case v < (1 << 30):
		return 4
	default:
		return -1 // value too large
	}
}

// Encode appends the encoded value of v to buf, and returns the updated slice.
// It panics if v is out of range.
func (v Vint30) Encode(buf []byte) []byte {
	s := v.EncodedLen()
	if s < 0 {
		panic("value out of range")
	}
	w := uint32(v)*4 + uint32(s-1)
	var tmp [4]byte
	for i := 0; i < s; i++ {
		tmp[i] = byte(w % 256)
		w /= 256
	}
	return append(buf, tmp[:s]...)
}

// ParseVint30 decodes a prefix of buf as a Vint30, and reports the number of
// bytes consumed by the encoding. If buf does not begin with a valid encoding,
// it returns -1, 0.
func ParseVint30(buf []byte) (int, Vint30) {
	if len(buf) == 0 {
		return -1, 0 // no valid encoding
	}
	s := int(buf[0]%4) + 1
	if len(buf) < s {
		return -1, 0 // incomplete value
	}
	var w uint32
	for i := s - 1; i >= 0; i-- {
		w = (w * 256) + uint32(buf[i])
	}
	return s, Vint30(w / 4)
}

// Bytes is a slice of bytes that encodes to a Vint30 prefix followed by the
// contents of the specified slice.
type Bytes []byte

// EncodedLen reports the number of bytes needed to encode b.
// If b is too long to be represented, EncodedLen returns -1.
func (b Bytes) EncodedLen() int {
	if len(b) > MaxVint30 {
		return -1
	}
	return Vint30(len(b)).EncodedLen() + len(b)
}

// Encode appends the encoding of b to buf, and returns the updated slice.
// It panics if b is too long to be encoded.
func (b Bytes) Encode(buf []byte) []byte {
	out := Vint30(len(b)).Encode(buf)
	return append(out, b...)
}

// ParseBytes decodes a length-prefixed sliced of bytes from the front of buf
// and reports the number of bytes consumed by the encoding. If buf does not
// begin with a valud encoding, it returns -1, nil.
//
// A successful ParseBytes returns a slice that aliases input array.  The
// caller must copy the bytes if the underlying data are expected to change.
func ParseBytes(buf []byte) (int, Bytes) {
	nb, blen := ParseVint30(buf)
	if nb < 0 {
		return -1, nil // invalid length prefix
	}
	end := nb + int(blen)
	if len(buf) < end {
		return -1, nil // data are truncated
	}
	return end, buf[nb:end]
}

// Literal is a literal string that encodes as the sequence of bytes comprising
// the string without padding or framing.
type Literal string

// EncodedLen reports the number of bytes needed to encode s, which is equal
// to the length of s in bytes.
func (s Literal) EncodedLen() int { return len(s) }

// Encode appends the encoded value of s to buf and returns the updated slice.
func (s Literal) Encode(buf []byte) []byte { return append(buf, s...) }

// ParseLiteral decodes the specified string from the front of buf, and reports
// the number of bytes consumed. If len(buf) < len(s) or the prefix is not
// equal to s, it returns -1, nil. The slice returned shares storage with buf.
func ParseLiteral(s string, buf []byte) (int, []byte) {
	if len(buf) < len(s) || string(buf[:len(s)]) != s {
		return -1, nil
	}
	return len(s), buf[:len(s)]
}

// Bool is a bool that encodes as a single byte with value 0 or 1.
type Bool bool

// EncodedLen reports the number of bytes needed to encode b, which is 1.
func (Bool) EncodedLen() int { return 1 }

// Encode appends the encoded value of b to buf and returns the updated slice.
func (b Bool) Encode(buf []byte) []byte {
	if b {
		return append(buf, 1)
	}
	return append(buf, 0)
}

// ParseBool decodes a single byte from the front of buf as a Bool, and reports
// the number of bytes consumed (1), or -1 if buf is empty. A 0 means false and
// any non-zero value means true.
func ParseBool(buf []byte) (int, Bool) {
	switch {
	case len(buf) == 0:
		return -1, false
	case buf[0] == 0:
		return 1, false
	default:
		return 1, true
	}
}

// Raw is a slice of bytes that encodes literally without framing.
type Raw []byte

// EncodedLen reports the number of bytes needed to encode r, which is len(r).
func (r Raw) EncodedLen() int { return len(r) }

// Encode appends the bytes of r to buf, and returns the updated slice.
func (r Raw) Encode(buf []byte) []byte { return append(buf, r...) }

// ParseRaw decodes a slice of n bytes from the front of buf, and reports the
// number of bytes consumed. If len(buf) < n, it returns -1, nil.
func ParseRaw(n int, buf []byte) (int, []byte) {
	if len(buf) < n {
		return -1, nil
	}
	return n, buf[:n]
}

// An Encoder is a value that supports being encoded into binary form.
type Encoder interface {
	// EncodedLen reports the number of bytes needed to encode its receiver.
	// If the value cannot be encoded, EncodedLen must return -1.
	EncodedLen() int

	// Encode appends the encoded representation of the receiver to buf, and
	// returns the updated slice. If the value cannot be encoded, Encode must
	// panic.
	Encode([]byte) []byte
}

// A Slice is a sequence of Encoders that itself implements the Encoder
// interface.  It encodes as the concatenation of the encodings of its
// elements.
type Slice []Encoder

// EncodedLen implements the corresponding method of the Encoder interface.
// It reports the total length required to encode the elements of s, of -1 if
// any of the values cannot be encoded.
func (s Slice) EncodedLen() int {
	var sum int
	for _, v := range s {
		n := v.EncodedLen()
		if n < 0 {
			return -1
		}
		sum += n
	}
	return sum
}

// Encode implements the corresponding method of the Encoder interface.
// It panics if any element of s cannot be encoded.
// It returns buf unmodified if len(s) == 0.
func (s Slice) Encode(buf []byte) []byte {
	for _, v := range s {
		buf = v.Encode(buf)
	}
	return buf
}
