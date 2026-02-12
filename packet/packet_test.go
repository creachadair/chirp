// Copyright (C) 2024 Michael J. Fromberger. All Rights Reserved.

package packet_test

import (
	"testing"

	"github.com/creachadair/chirp/packet"
	"github.com/google/go-cmp/cmp"
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
		got := tc.input.Append(nil)
		if string(got) != tc.want {
			t.Errorf("Encode %d: got %v, want %v", tc.input, got, []byte(tc.want))
		}
		packed = tc.input.Append(packed) // see below

		// Make sure the value round-trips individually.
		s := packet.NewScanner(got)
		cmp, err := s.Vint30()
		if err != nil {
			t.Errorf("Scan: unexpected error: %v", err)
		} else if packet.Vint30(cmp) != tc.input {
			t.Errorf("Scan: got %v, want %v", cmp, tc.input)
		}
	}

	// Now decode the accumulated results to verify self-framing.
	t.Logf("Packed: %v", packed)
	s := packet.NewScanner(packed)
	var i int
	for s.Len() != 0 {
		got, err := s.Vint30()
		if err != nil {
			t.Fatalf("Invalid encoding at offset %d (%v)", s.Offset(), s.Rest())
		} else if i > len(tests) {
			t.Errorf("Index %d: got extra value %d (%v)", i, got, s.Rest())
		} else if packet.Vint30(got) != tests[i].input {
			t.Errorf("Index %d: got %v, want %v", i, got, tests[i].input)
		}
		i++
	}
}

func TestBuilder(t *testing.T) {
	var b packet.Builder
	b.Bool(true)
	b.Put(5, 9, 100)
	b.Uint16(5000)
	b.Uint32(0xfc009a01)
	b.Vint30(999)
	b.VPutString("apple")
	b.VPut([]byte("pear"))
	b.PutString("xyzzy")

	const want = "\x01\x05\x09\x64\x13\x88\xfc\x00\x9a\x01\x9d\x0f\x14apple\x10pearxyzzy"
	//             ^   ^---^---^-- ^-----  ^-------------- ^-----  ^------- ^------^----
	//          bool  byte*3        uint16  uint32          vint30  string   bytes literal

	if n := b.Len(); n != len(want) {
		t.Errorf("Len = %d, want %d", n, len(want))
	}
	if string(b.Bytes()) != want {
		t.Errorf("Bytes = %q, want %q", b.Bytes(), want)
	}

	s := packet.NewScanner(b.Bytes())
	check(t, "Bool", s.Bool, true)
	check(t, "Byte 1", s.Byte, 5)
	check(t, "Byte 2", s.Byte, 9)
	check(t, "Byte 3", s.Byte, 100)
	check(t, "Uint16", s.Uint16, 5000)
	check(t, "Uint32", s.Uint32, 0xfc009a01)
	check(t, "Vint30", s.Vint30, 999)
	check(t, "VString", func() (string, error) { return packet.VGet[string](s) }, "apple")
	check(t, "VBytes", func() ([]byte, error) { return packet.VGet[[]byte](s) }, []byte("pear"))
	check(t, "Literal", func() (string, error) { return packet.Get[string](s, 5) }, "xyzzy")

	if s.Len() != 0 {
		t.Errorf("Extra data at EOF (%d bytes): %q", s.Len(), s.Rest())
	}
}

func check[T any](t *testing.T, label string, f func() (T, error), want T) {
	t.Helper()

	got, err := f()
	if err != nil {
		t.Errorf("%s: unexpected error: %v", label, err)
	} else if diff := cmp.Diff(got, want); diff != "" {
		t.Errorf("%s result (-got, +want):\n%s", label, diff)
	}
}
