package packet_test

import (
	"strings"
	"testing"

	"github.com/creachadair/chirp/packet"
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
		got := tc.input.Encode()
		if string(got) != tc.want {
			t.Errorf("Encode %d: got %v, want %v", tc.input, got, []byte(tc.want))
		}
		packed = tc.input.Append(packed) // see below

		// Make sure the value round-trips individually.
		nb, cmp := packet.DecodeVint30(got)
		if nb != len(tc.want) || cmp != tc.input {
			t.Errorf("Decode: got value %d (%d bytes), want %d (%d bytes)", cmp, nb, tc.input, len(tc.want))
		}
	}

	// Now decode the accumulated results to verify self-framing.
	t.Logf("Packed: %v", packed)
	for i := 0; len(packed) != 0; i++ {
		nb, got := packet.DecodeVint30(packed)
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
		nb, got := packet.DecodeBytes([]byte(tc.input))
		if nb != tc.nb || string(got) != tc.want {
			t.Errorf("Decode %q: got (%q, %d), want (%q, %d)", tc.input, got, nb, tc.want, tc.nb)
		} else if nb < 0 {
			continue
		}

		all = append(all, tc.input...) // see below

		// If the result was valid, make sure it round-trips correctly.
		cmp := packet.Bytes(got).Encode()
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

		nb, got := packet.DecodeBytes(all)
		if nb < 0 {
			t.Fatalf("Index %d: decode failed", i)
		} else if string(got) != tests[i].want {
			t.Errorf("Index %d: got %q, want %q", i, got, tests[i].want)
		}
		all = all[nb:]
	}
}
