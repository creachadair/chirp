// Copyright (C) 2024 Michael J. Fromberger. All Rights Reserved.

package packet_test

import (
	"strings"
	"testing"

	"github.com/creachadair/chirp/packet"
	"github.com/google/go-cmp/cmp"
)

// Satisfaction checks.
var (
	_ packet.Encoder = packet.Vint30(0)
	_ packet.Encoder = packet.Bytes(nil)
	_ packet.Encoder = packet.Literal("")
	_ packet.Encoder = packet.Bool(false)
	_ packet.Encoder = packet.Raw(nil)
	_ packet.Encoder = packet.Slice(nil)

	_ packet.Decoder = (*packet.Vint30)(nil)
	_ packet.Decoder = (*packet.Bytes)(nil)
	_ packet.Decoder = packet.Literal("")
	_ packet.Decoder = (*packet.Bool)(nil)
	_ packet.Decoder = (*packet.Raw)(nil)
)

func TestVint30(t *testing.T) {
	tests := []struct {
		input packet.Vint30
		want  string
	}{
		// Single-byte encodings.
		{0, "\x00"},
		{1, "\x04"},
		{63, "\xfc"},

		// Two-byte encodings.
		{64, "\x01\x01"},
		{100, "\x91\x01"},
		{500, "\xd1\x07"},
		{16383, "\xfd\xff"},

		// Three-byte encodings.
		{16384, "\x02\x00\x01"},
		{65000, "\xa2\xf7\x03"},
		{1048576, "\x02\x00\x40"},

		// Four-byte encodings.
		{62830181, "\x97\xd9\xfa\x0e"},
		{536896023, "\x5f\x88\x01\x80"},
		{1073741823, "\xff\xff\xff\xff"}, // maximum supported value
	}

	var packed []byte
	for _, tc := range tests {
		got := tc.input.Encode(nil)
		if string(got) != tc.want {
			t.Errorf("Encode %d: got %v, want %v", tc.input, got, []byte(tc.want))
		}
		packed = tc.input.Encode(packed) // see below

		// Make sure the value round-trips individually.
		nb, cmp := packet.ParseVint30(got)
		if nb != len(tc.want) || cmp != tc.input {
			t.Errorf("Parse: got value %d (%d bytes), want %d (%d bytes)", cmp, nb, tc.input, len(tc.want))
		}
	}

	// Now decode the accumulated results to verify self-framing.
	t.Logf("Packed: %v", packed)
	for i := 0; len(packed) != 0; i++ {
		nb, got := packet.ParseVint30(packed)
		if nb < 0 {
			t.Fatalf("Invalid encoding at %v", packed)
		} else if i > len(tests) {
			t.Errorf("Index %d: got extra value %d (%d bytes)", i, got, nb)
		} else if got != tests[i].input {
			t.Errorf("Index %d: got %d, want %d", i, got, tests[i].input)
		}
		packed = packed[nb:]
	}
}

func TestBytes(t *testing.T) {
	buf17K := strings.Repeat("q", 17000)
	tests := []struct {
		input string
		nb    int
		want  string
	}{
		{"", -1, ""},         // no valid encoding (empty)
		{"\x03\x00", -1, ""}, // invalid length prefix (wants 4 bytes)
		{"\x08x", -1, ""},    // valid length, but short data

		{"\x00", 1, ""},           // valid encoding, empty data (1-byte length)
		{"\x14hello", 6, "hello"}, // valid encoding, non-empty data (1-byte length)

		// A 63-byte buffer, the maximum that will fit in a 1-byte length.
		{"\xfca clever inconsistency is the panacea of expansive intelligence", 64,
			"a clever inconsistency is the panacea of expansive intelligence"},

		// A 71-byte buffer, requires a 2-byte length.
		{"\x1d\x01why are there so many songs about rainbows and what's on the other side", 73,
			"why are there so many songs about rainbows and what's on the other side"},

		// A 17000-byte buffer, requires a 3-byte length.
		{"\xa2\x09\x01" + buf17K, 17000 + 3, buf17K},
	}

	var all []byte
	for _, tc := range tests {
		nb, got := packet.ParseBytes([]byte(tc.input))
		if nb != tc.nb || string(got) != tc.want {
			t.Errorf("Parse %q: got (%q, %d), want (%q, %d)", tc.input, got, nb, tc.want, tc.nb)
		} else if nb < 0 {
			continue
		}

		all = append(all, tc.input...) // see below

		// If the result was valid, make sure it round-trips correctly.
		cmp := packet.Bytes(got).Encode(nil)
		if string(cmp) != tc.input {
			t.Errorf("Encode %v: got %v, want %v", got, cmp, []byte(tc.input))
		}
	}

	// Now decode the accumulated results to verify self-framing.
	for i := 0; len(all) != 0; i++ {
		if i > len(tests) {
			t.Fatalf("Index %d: extra input after tests", i)
		} else if tests[i].nb < 0 {
			continue // skip, we didn't add anything for this case
		}

		nb, got := packet.ParseBytes(all)
		if nb < 0 {
			t.Fatalf("Index %d: decode failed", i)
		} else if string(got) != tests[i].want {
			t.Errorf("Index %d: got %q, want %q", i, got, tests[i].want)
		}
		all = all[nb:]
	}
}

func TestRaw(t *testing.T) {
	const text = "0123456789"
	tests := []struct {
		input packet.Raw
		nb    int
		want  string
	}{
		{nil, 0, ""},                    // ok, empty
		{packet.Raw{}, 0, ""},           // ok, empty
		{packet.Raw("abcd"), 4, "0123"}, // ok, nonempty
		{packet.Raw("---------------"), -1, "---------------"}, // value doesn't change
	}

	buf := []byte(text)
	for _, tc := range tests {
		nb := tc.input.Decode(buf)
		if nb != tc.nb {
			t.Errorf("Decode %v: got %d, want %d", tc.input, nb, tc.nb)
		} else if string(tc.input) != tc.want {
			t.Errorf("Decode: got %q, want %q", tc.input, tc.want)
		}
		if tc.nb <= 0 {
			continue
		}

		// Verify that we can round-trip the value.
		if enc := string(tc.input.Encode(nil)); enc != tc.want {
			t.Errorf("Encode %v: got %q, want %q", tc.input, enc, tc.want)
		}

		// Verify that we got a copy.
		tc.input[0] = 255
		if string(buf) != text {
			t.Errorf("Value is shared with the input")
		}
	}
}

func TestSlice(t *testing.T) {
	const text = "go away or I shall taunt you a second time"
	vs := packet.Slice{
		packet.Vint30(1000),
		packet.Vint30(8000),
		packet.Vint30(98765432),
		packet.Vint30(14),
	}
	s := packet.Slice{
		packet.Literal("OK"),
		packet.Bytes([]byte(text)),
		packet.Bool(true),
		packet.Vint30(len(vs)),
		vs,
	}
	enc := s.Encode(nil)
	t.Logf("Encoding: %#q", enc)

	if nb, got := packet.ParseLiteral("OK", enc); nb < 0 {
		t.Fatal("ParseLiteral: not found")
	} else if string(got) != "OK" {
		t.Errorf("ParseLiteral: got %q, want OK", got)
	} else {
		enc = enc[nb:]
	}
	if nb, got := packet.ParseBytes(enc); nb < 0 {
		t.Fatal("ParseBytes: not found")
	} else if string(got) != text {
		t.Errorf("ParseBytes: got %q, want %q", got, text)
	} else {
		enc = enc[nb:]
	}
	if nb, got := packet.ParseBool(enc); nb < 0 {
		t.Fatal("ParseBool: not found")
	} else if got != true {
		t.Errorf("ParseBool: got %v, want true", got)
	} else {
		enc = enc[nb:]
	}
	nb, nelt := packet.ParseVint30(enc)
	if nb < 0 {
		t.Fatal("ParseVint30: not found")
	}
	enc = enc[nb:]

	var vals packet.Slice
	for nelt > 0 {
		nb, elt := packet.ParseVint30(enc)
		if nb < 0 {
			t.Fatalf("Element %d: not found", nelt)
		}
		vals = append(vals, elt)
		enc = enc[nb:]
		nelt--
	}
	if diff := cmp.Diff(vals, vs); diff != "" {
		t.Errorf("Values (-got, +want):\n%s", diff)
	}
	if len(enc) != 0 {
		t.Errorf("At EOF, encoding has leftover data: %v", enc)
	}
}

func TestParse(t *testing.T) {
	want := packet.Slice{
		packet.Bytes("fee fi fo fum"),
		packet.Vint30(12345),
		packet.Literal("OK"),
		packet.Vint30(67890),
	}

	enc := want.Encode(nil)
	t.Logf("Encoding: %#q", enc)

	var b packet.Bytes
	var v1, v2 packet.Vint30
	nr, err := packet.Parse(enc, &b, &v1, packet.Literal("OK"), &v2)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}

	// The parse should have consumed the whole input.
	if nr != len(enc) {
		t.Errorf("Parse: used %d bytes, want %d", nr, len(enc))
	}

	if diff := cmp.Diff(packet.Slice{b, v1, packet.Literal("OK"), v2}, want); diff != "" {
		t.Errorf("Parse (-got, +want):\n%s", diff)
	}
}
