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

// Encode returns the encoded form of v. It panics if v is out of range.
// This is a shorthand for v.Append(nil).
func (v Vint30) Encode() []byte { return v.Append(nil) }

// Append appends the encoded value of v to buf, and returns the updated slice.
// It panics if v is out of range.
func (v Vint30) Append(buf []byte) []byte {
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

// DecodeVint30 decodes a prefix of buf as a Vint30, and reports the number of
// bytes consumed by the encoding. If buf does not begin with a valid encoding,
// it returns -1, 0.
func DecodeVint30(buf []byte) (int, Vint30) {
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

// Encode returns the encoded form of b. It panics if b is too long to be
// encoded. This is a shorthand for b.Append(nil).
func (b Bytes) Encode() []byte { return b.Append(nil) }

// Append appends the encoding of b to buf, and returns the updated slice.
// It panics if b is too long to be encoded.
func (b Bytes) Append(buf []byte) []byte {
	out := Vint30(len(b)).Append(buf)
	return append(out, b...)
}

// DecodeBytes decodes a length-prefixed sliced of bytes from the front of buf
// and reports the number of bytes consumed by the encoding. If buf does not
// begin with a valud encoding, it returns -1, nil.
//
// A successful DecodeBytes returns a slice that aliases input array.  The
// caller must copy the bytes if the underlying data are expected to change.
func DecodeBytes(buf []byte) (int, []byte) {
	nb, blen := DecodeVint30(buf)
	if nb < 0 {
		return -1, nil // invalid length prefix
	}
	end := nb + int(blen)
	if len(buf) < end {
		return -1, nil // data are truncated
	}
	return end, buf[nb:end]
}
