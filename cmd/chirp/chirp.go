// Program chirp is a command-line utility for interacting with Chirp v0 peers.
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/creachadair/chirp/packet"
	"github.com/creachadair/command"
)

func main() {
	root := &command.C{
		Name: filepath.Base(os.Args[0]),
		Help: "Utilities for interacting with Chirp v0 peers.",
		Commands: []*command.C{
			{
				Name:  "pack",
				Usage: "<pattern> <argument>...",
				Help: `Pack arguments into a binary packet.

The pattern specifies the sequence of values to concatenate into the packet.
Whitespace in the pattern is ignored; otherwise the pattern specifies how the
corresponding argument is processed:

  p  : a Pascal style string with a 1-byte length prefix
  q  : a quoted literal string (Go style) without framing
  r  : a raw literal string encoded without framing
  s  : a string encoded with a vint30 length prefix
  %  : a Boolean constant (true or false)
  v  : a vint30 value (unsigned)
  1  : a uint8 value (1 byte)
  2  : a uint16 value (2 bytes)
  4  : a uint32 value (4 bytes)
  8  : a uint64 value (8 bytes)

By default, fixed-width integer values are packed in big-endian order, but the
following symbols modify the byte order for future values:

  <  : encode as little-endian
  >  : encode as big-endian (this is the default)

In addition, a "(" begins a subpattern, which goes until a matching ")".
Nested subpatterns are permitted.  Each subpattern is encoded according to its
contents, with a length prefix prepended. By default, the length prefix is a vint30,
but the following symbols modify the length encoding for future subpatterns:

  @  : encode length as a uint16 (2 bytes)
  $  : encode length as a uint32 (4 bytes)
  *  : encode length as a uint64 (8 bytes)
  ?  : encode length as a vint30 (this is the default)

Subpatterns may be nested.
`,
				Run: func(env *command.Env) error {
					if len(env.Args) == 0 {
						return env.Usagef("Missing format argument")
					}
					enc, rest, err := formatData(env.Args[0], env.Args[1:])
					if err != nil {
						return err
					} else if len(rest) != 0 {
						return fmt.Errorf("extra arguments: %q", rest)
					}
					os.Stdout.Write(enc.Encode(nil))
					return nil
				},
			},
			command.VersionCommand(),
			command.HelpCommand(nil),
		},
	}
	command.RunOrFail(root.NewEnv(nil).MergeFlags(true), os.Args[1:])
}

func formatData(pat string, args []string) (packet.Slice, []string, error) {
	size := byte('$')
	var byteOrder binary.AppendByteOrder = binary.BigEndian
	packSize := func(n int) packet.Encoder {
		switch size {
		case '?':
			return packet.Vint30(n)
		case '@':
			return packet.Raw(byteOrder.AppendUint16(nil, uint16(n)))
		case '$':
			return packet.Raw(byteOrder.AppendUint32(nil, uint32(n)))
		case '*':
			return packet.Raw(byteOrder.AppendUint64(nil, uint64(n)))
		default:
			panic("invalid size type: " + string(size))
		}
	}
	var enc packet.Slice
	for i := 0; i < len(pat); i++ {
		c := pat[i]
		switch c {
		case 'p', 'q', 'r', 's', '%', 'v', '1', '2', '4', '8':
			// OK, these need an argument (see below)
		case ' ', '\t', '\n':
			// Skip whitespace.
			continue
		case '@', '$', '*', '?':
			// Set sub-pattern size encoding.
			size = c
			continue
		case '<':
			byteOrder = binary.LittleEndian
			continue
		case '>':
			byteOrder = binary.BigEndian
			continue
		case '(':
			// Sub-pattern (sub) becomes vdata of the contents.
			sub, ok := cutParen(pat[i+1:], '(', ')')
			if !ok {
				return nil, nil, errors.New("missing close parenthesis")
			}
			sd, sa, err := formatData(sub, args)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid subpattern: %w", err)
			}
			enc = append(enc, packSize(sd.EncodedLen()), sd)
			args = sa
			i += len(sub) + 1
			continue
		default:
			return nil, nil, fmt.Errorf("invalid pattern word %c", c)
		}

		if len(args) == 0 {
			return nil, nil, fmt.Errorf("missing argument for %c", c)
		}
		switch c {
		case 'p':
			if len(args[0]) > 255 {
				return nil, nil, fmt.Errorf("length %d > 255 too long for p", len(args[0]))
			}
			enc = append(enc, packet.Raw([]byte{byte(len(args[0]))}), packet.Literal(args[0]))
		case 'q':
			dec, err := strconv.Unquote(`"` + args[0] + `"`)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid string: %w", err)
			}
			enc = append(enc, packet.Literal(dec))
		case 'r':
			enc = append(enc, packet.Literal(args[0]))
		case 's':
			enc = append(enc, packet.Bytes(args[0]))
		case '%':
			v, err := strconv.ParseBool(args[0])
			if err != nil {
				return nil, nil, fmt.Errorf("invalid bool: %w", err)
			}
			enc = append(enc, packet.Bool(v))
		case 'v':
			v, err := strconv.ParseUint(args[0], 10, 30)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid vint30: %w", err)
			}
			enc = append(enc, packet.Vint30(v))
		case '1':
			v, err := strconv.ParseUint(args[0], 10, 8)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid byte: %w", err)
			}
			enc = append(enc, packet.Raw([]byte{byte(v)}))
		case '2':
			v, err := strconv.ParseUint(args[0], 10, 16)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid uint16: %w", err)
			}
			enc = append(enc, packet.Raw(byteOrder.AppendUint16(nil, uint16(v))))
		case '4':
			v, err := strconv.ParseUint(args[0], 10, 32)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid uint32: %w", err)
			}
			enc = append(enc, packet.Raw(byteOrder.AppendUint32(nil, uint32(v))))
		case '8':
			v, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid uint64: %w", err)
			}
			enc = append(enc, packet.Raw(byteOrder.AppendUint64(nil, v)))
		default:
			panic("invalid code: " + string(c))
		}
		args = args[1:]
	}
	return enc, args, nil
}

func cutParen(s string, l, r rune) (string, bool) {
	d := 1
	for i, c := range s {
		if c == l {
			d++
		} else if c == r {
			d--
			if d == 0 {
				return s[:i], true
			}
		}
	}
	return s, false
}
