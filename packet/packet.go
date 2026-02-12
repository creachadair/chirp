// Copyright (C) 2024 Michael J. Fromberger. All Rights Reserved.

// Package packet provides support for encoding and decoding binary packet data.
package packet

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/creachadair/mds/value"
)

// A Builder is a buffer that accumulates data into a packet. The zero value is
// ready for use as an empty builder.
type Builder struct {
	buf []byte
}

// Bool appends a Boolean to b. The encoding is a single byte with value 0 or 1.
func (b *Builder) Bool(ok bool) { b.Put(value.Cond[byte](ok, 1, 0)) }

// Put appends the specified bytes to v in order.
func (b *Builder) Put(vs ...byte) { b.buf = append(b.buf, vs...) }

// PutString appends the specified string to v.
func (b *Builder) PutString(s string) { b.buf = append(b.buf, s...) }

// VPut appends a length-prefixed string to b. The length is encoded as a [Vint30].
func (b *Builder) VPut(vs []byte) {
	b.Grow(VLen(len(vs)))
	b.Vint30(uint32(len(vs)))
	b.buf = append(b.buf, vs...)
}

// VPutString appends a length-prefixed string to b. The length is encoded as a [Vint30].
func (b *Builder) VPutString(s string) {
	b.Grow(VLen(len(s)))
	b.Vint30(uint32(len(s)))
	b.buf = append(b.buf, s...)
}

// Uint16 appends v to b in big-endian order.
func (b *Builder) Uint16(v uint16) { b.buf = binary.BigEndian.AppendUint16(b.buf, v) }

// Uint32 appends v to b in big-endian order.
func (b *Builder) Uint32(v uint32) { b.buf = binary.BigEndian.AppendUint32(b.buf, v) }

// Vint30 appends a [Vint30] value to b.
func (b *Builder) Vint30(v uint32) { b.buf = Vint30(v).Append(b.buf) }

// Len reports the number of bytes currently in the buffer.
func (b *Builder) Len() int { return len(b.buf) }

// Bytes reports the current contents of the buffer. The builder retains ownership
// of the reported slice, and the caller must not retain or modify its contents
// unless b will no longer be accessed.
func (b *Builder) Bytes() []byte { return b.buf }

// Reset discards the contents of b and leaves it empty.
func (b *Builder) Reset() { b.buf = b.buf[:0] }

// Grow resizes the internal buffer of b if necessary to ensure that at least n
// more bytes can be added without triggering another allocation.
func (b *Builder) Grow(n int) {
	want := len(b.buf) + n
	if cap(b.buf) < want {
		r := make([]byte, len(b.buf), max(want, 2*cap(b.buf)))
		copy(r, b.buf)
		b.buf = r
	}
}

// A Scanner reads encoded values from the contents of a packet.
// The methods of a scanner return [io.EOF] when no further input is available.
// Incomplete values report [io.ErrUnexpectedEOF].
type Scanner struct {
	input  []byte
	rest   []byte
	offset int // of reset from input
}

// NewScanner constructs a [Scanner] that consumes data from input.
// The scanner does not modify the contents of input, but retain slices
// into it, so the caller should ensure it is not modified while the scanner
// is in use.
func NewScanner[Str ~string | ~[]byte](input Str) *Scanner {
	data := []byte(input)
	return &Scanner{input: data, rest: data}
}

// Bool scans a single byte from the head of the input and converts it into a
// Boolean value (0 means false, non-zero means true).
func (s *Scanner) Bool() (bool, error) {
	b, err := s.Byte()
	if err != nil {
		return false, err
	}
	return b != 0, nil
}

// Byte scans a single byte from the head of the input.
func (s *Scanner) Byte() (byte, error) {
	if len(s.rest) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	s.offset++
	out := s.rest[0]
	s.rest = s.rest[1:]
	return out, nil
}

// VLen reports the encoded size in bytes of a length-prefixed encoding of an
// n-byte string, where the length is encoded as a [Vint30].
func VLen(n int) int { return Vint30(n).Size() + n }

// Vint30 parses a single [Vint30] value from the head of the input.
func (s *Scanner) Vint30() (int, error) {
	if len(s.rest) == 0 {
		return 0, io.EOF
	}
	nb := int(s.rest[0]%4) + 1
	if len(s.rest) < nb {
		return 0, io.ErrUnexpectedEOF
	}
	var w uint32
	for i := nb - 1; i >= 0; i-- {
		w = (w * 256) + uint32(s.rest[i])
	}
	s.offset += nb
	s.rest = s.rest[nb:]
	return int(w >> 2), nil
}

// Uint16 parses a big-endian uint16 value from the head of the input.
func (s *Scanner) Uint16() (uint16, error) {
	if len(s.rest) < 2 {
		return 0, fmt.Errorf("value truncate (%d < 2 bytes): %w", len(s.rest), io.ErrUnexpectedEOF)
	}
	s.offset += 2
	out := binary.BigEndian.Uint16(s.rest[:2])
	s.rest = s.rest[2:]
	return out, nil
}

// Uint32 parses a big-endian uint32 value from the head of the input.
func (s *Scanner) Uint32() (uint32, error) {
	if len(s.rest) < 4 {
		return 0, fmt.Errorf("value truncated (%d < 4 bytes): %w", len(s.rest), io.ErrUnexpectedEOF)
	}
	s.offset += 4
	out := binary.BigEndian.Uint32(s.rest[:4])
	s.rest = s.rest[4:]
	return out, nil
}

// Len reports the number of remaining unconsumed input bytes in s.
func (s *Scanner) Len() int { return len(s.rest) }

// Offset reports the offset (0-based) of the next unconsumed input byte in s.
func (s *Scanner) Offset() int { return s.offset }

// Rest returns a slice of the remaining unconsumed input of s.
// The reported slice is only valid until the next call to a method of s,
// and the caller must not modify its contents.
func (s *Scanner) Rest() []byte { return s.rest }

// VGet parses a single length-prefixed string from the head of s.
// The length must be encoded as a [Vint30].
// When the result is a slice, the value aliases the input, and the caller must
// not modify its contents.
func VGet[Str ~string | ~[]byte](s *Scanner) (out Str, err error) {
	nb, err := s.Vint30()
	if err != nil {
		return out, err
	}
	if len(s.rest) < nb {
		return out, fmt.Errorf("value truncated (%d < %d bytes): %w", len(s.rest), nb, io.ErrUnexpectedEOF)
	}
	s.offset += nb
	out = Str(s.rest[:nb])
	s.rest = s.rest[nb:]
	return out, nil
}

// Get returns a string of exactly n bytes from the head of the input.
// If the full requested amount is not available, a partial result is returned
// along with an error.  When the result is a slice, the value aliases the
// input, and the caller must not modify its contents.
func Get[Str ~string | ~[]byte](s *Scanner, n int) (Str, error) {
	if len(s.rest) < n {
		return Str(s.rest), fmt.Errorf("value truncated (%d < %d bytes): %w", len(s.rest), n, io.ErrUnexpectedEOF)
	}
	s.offset += n
	out := Str(s.rest[:n])
	s.rest = s.rest[n:]
	return out, nil
}

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

// Size reports the number of bytes required to encode v. If v is too large to
// be encoded, Size returns -1.
func (v Vint30) Size() int {
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

// Append appends the encoded value of v to buf, and returns the updated slice.
// It panics if v is out of range.
func (v Vint30) Append(buf []byte) []byte {
	s := v.Size()
	if s < 0 {
		panic("value out of range")
	}
	w := uint32(v)*4 + uint32(s-1)
	var tmp [4]byte
	for i := range s {
		tmp[i] = byte(w % 256)
		w /= 256
	}
	return append(buf, tmp[:s]...)
}
