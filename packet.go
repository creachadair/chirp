// Copyright (C) 2022 Michael J. Fromberger. All Rights Reserved.

package chirp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/creachadair/mds/mstr"
)

// Packet is the parsed format of a Chirp v0 packet.
type Packet struct {
	Protocol byte
	Type     PacketType
	Payload  []byte
}

// WriteTo writes the packet to w in binary format. It satisfies [io.WriterTo].
func (p Packet) WriteTo(w io.Writer) (int64, error) {
	buf := make([]byte, 0, 8+len(p.Payload))
	buf = append(buf, '\xc7', p.Protocol, byte(p.Type>>8), byte(p.Type&255))
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(p.Payload)))
	buf = append(buf, p.Payload...)

	nw, err := w.Write(buf)
	return int64(nw), err
}

// ReadFrom reads a packet from r in binary format. It satisfies [io.ReaderFrom].
func (p *Packet) ReadFrom(r io.Reader) (int64, error) {
	var buf [8]byte
	nr, err := io.ReadFull(r, buf[:])
	if err != nil {
		return int64(nr), fmt.Errorf("short packet header: %w", err)
	}
	if buf[0] != '\xc7' {
		return int64(nr), fmt.Errorf("invalid protocol magic '%02x'", buf[0])
	}
	p.Protocol = buf[1]
	p.Type = PacketType(uint16(buf[2])*256 + uint16(buf[3]))
	p.Payload = nil

	if psize := binary.BigEndian.Uint32(buf[4:]); psize > 0 {
		p.Payload = make([]byte, int(psize))
		np, err := io.ReadFull(r, p.Payload)
		if err != nil {
			return int64(nr + np), fmt.Errorf("short payload: %w", err)
		}
		nr += np
	}
	return int64(nr), nil
}

// String returns a human-friendly rendering of the packet.
func (p *Packet) String() string {
	var pay string
	switch p.Type {
	case PacketRequest:
		var req Request
		if err := req.Decode(p.Payload); err == nil {
			pay = req.String()
		}
	case PacketCancel:
		var can Cancel
		if err := can.Decode(p.Payload); err == nil {
			pay = can.String()
		}
	case PacketResponse:
		var rsp Response
		if err := rsp.Decode(p.Payload); err == nil {
			pay = rsp.String()
		}
	}
	if pay == "" {
		pay = trimData(p.Payload)
	}
	return fmt.Sprintf("Packet(v%v, %v, %s)", p.Protocol, p.Type, pay)
}

// PacketType describes the structure type of a Chirp v0 packet.
//
// All packet type values from 0 to 127 inclusive are reserved by the protocol
// and MUST NOT be used for any other purpose. Packet type values from 128 to
// 65535 are reserved for use by the implementation.
type PacketType uint16

const (
	PacketRequest  PacketType = 2 // The initial request for a call
	PacketCancel   PacketType = 3 // A cancellation signal for a pending call
	PacketResponse PacketType = 4 // The final response from a call

	maxReservedType = 127
)

func (p PacketType) String() string {
	switch p {
	case PacketRequest:
		return "REQUEST"
	case PacketCancel:
		return "CANCEL"
	case PacketResponse:
		return "RESPONSE"
	default:
		return fmt.Sprintf("TYPE:%d", byte(p))
	}
}

// Request is the payload format for a Chirp v0 request packet.
type Request struct {
	RequestID uint32
	Method    string // at most MaxMethodLen bytes
	Data      []byte
}

// MaxMethodLen is the maximum permitted length for a method name.
const MaxMethodLen = 255

// Encode encodes the request data in binary format.
// This method will panic if r.Method is longer than [MaxMethodLen].
func (r Request) Encode() []byte {
	mlen := len(r.Method)
	if mlen > 255 {
		panic(fmt.Sprintf("method name is %d (>255) bytes", mlen))
	}
	buf := make([]byte, 0, 4+1+mlen+len(r.Data)) // 4 request ID, 1 method name length
	buf = binary.BigEndian.AppendUint32(buf, r.RequestID)
	buf = append(buf, byte(mlen))
	buf = append(buf, r.Method...)
	buf = append(buf, r.Data...)
	return buf
}

// Decode decodes data into a Chirp v0 request payload.
func (r *Request) Decode(data []byte) error {
	if len(data) < 5 { // 4 request ID, 1 method name length
		return fmt.Errorf("short request payload (%d bytes)", len(data))
	}
	r.RequestID = binary.BigEndian.Uint32(data[0:])
	mlen := int(data[4])
	if 5+mlen > len(data) {
		return fmt.Errorf("short method name (%d > %d bytes)", 5+mlen, len(data))
	}
	r.Method = string(data[5 : 5+mlen])
	r.Data = nil
	if len(data[5+mlen:]) > 0 {
		r.Data = data[5+mlen:]
	}
	return nil
}

// String returns a human-friendly rendering of the request.
func (r Request) String() string {
	return fmt.Sprintf("Request(ID=%v, M=%q, %s)", r.RequestID, r.Method, trimData(r.Data))
}

// Response is the payload format for a Chirp v0 response packet.
type Response struct {
	RequestID uint32
	Code      ResultCode
	Data      []byte
}

// Encode encodes the response data in binary format.
func (r Response) Encode() []byte {
	buf := make([]byte, 0, 4+1+len(r.Data)) // 4 request ID, 1 code
	buf = binary.BigEndian.AppendUint32(buf, r.RequestID)
	buf = append(buf, byte(r.Code))
	return append(buf, r.Data...)
}

// Decode decodes data into a Chirp v0 response payload.
func (r *Response) Decode(data []byte) error {
	if len(data) < 5 { // 4 request ID, 1 code
		return fmt.Errorf("short response payload (%d bytes)", len(data))
	}
	r.RequestID = binary.BigEndian.Uint32(data[0:])
	r.Code = ResultCode(data[4])
	if r.Code > CodeServiceError {
		return fmt.Errorf("invalid result code %d", r.Code)
	}
	if len(data[5:]) > 0 {
		r.Data = data[5:]
	} else {
		r.Data = nil
	}
	return nil
}

// String returns a human-friendly rendering of the response.
func (r Response) String() string {
	var data string
	if r.Code == CodeServiceError {
		var ed ErrorData
		if ed.Decode(r.Data) == nil {
			data = fmt.Sprintf("ErrorData(C=%d, [%d bytes], %q)", ed.Code, len(ed.Data), ed.Message)
		}
	}
	if data == "" {
		data = trimData(r.Data)
	}
	return fmt.Sprintf("Response(ID=%v, C=%v, %s)", r.RequestID, r.Code, data)
}

// ResultCode describes the result status of a completed call.  All result
// codes not defined here are reserved for future use by the protocol.
type ResultCode byte

const (
	CodeSuccess       ResultCode = 0 // Call completed succesfully
	CodeUnknownMethod ResultCode = 1 // Requested an unknown method
	CodeDuplicateID   ResultCode = 2 // Duplicate request ID
	CodeCanceled      ResultCode = 3 // Call was canceled
	CodeServiceError  ResultCode = 4 // Call failed due to a service error
)

func (c ResultCode) String() string {
	switch c {
	case CodeSuccess:
		return "SUCCESS"
	case CodeUnknownMethod:
		return "UNKNOWN_METHOD"
	case CodeDuplicateID:
		return "DUPLICATE_REQUEST_ID"
	case CodeCanceled:
		return "CANCELED"
	case CodeServiceError:
		return "SERVICE_ERROR"
	default:
		return fmt.Sprintf("result code %d", byte(c))
	}
}

// Cancel is the payload format for a Chirp v0 cancel request packet.
type Cancel struct {
	RequestID uint32
}

// Encode encodes the cancel request data in binary format.
func (c Cancel) Encode() []byte {
	var buf [4]byte
	return binary.BigEndian.AppendUint32(buf[:0], c.RequestID)
}

// Decode decodes data into a Chirp v0 cancel payload.
func (c *Cancel) Decode(data []byte) error {
	if len(data) != 4 {
		return fmt.Errorf("invalid cancel payload (%d bytes)", len(data))
	}
	c.RequestID = binary.BigEndian.Uint32(data)
	return nil
}

// String returns a human-friendly rendering of the cancellation.
func (c Cancel) String() string { return fmt.Sprintf("Cancel(ID=%v)", c.RequestID) }

// ErrorData is the response data format for a service error response.
//
// An ErrorData value satisfies the error interface, allowing a handler to
// return one to control the error code and ancillary data reported.
type ErrorData struct {
	Code    uint16
	Message string
	Data    []byte
}

// Error implements the error interface, allowing an ErrorData value to be used
// as an error. This can be used by method handlers to control the error code
// and auxiliary data reported to the caller.
func (e ErrorData) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("[code %d] %s", e.Code, e.Message)
	}
	return e.Message
}

// Encode encodes the error data in binary format.
func (e ErrorData) Encode() []byte {
	msg := mstr.Trunc(e.Message, 65535)
	mlen := len(msg)

	buf := make([]byte, 0, 4+mlen+len(e.Data)) // 2 code, 2 length
	buf = binary.BigEndian.AppendUint16(buf, e.Code)
	buf = binary.BigEndian.AppendUint16(buf, uint16(mlen))
	buf = append(buf, msg...)
	return append(buf, e.Data...)
}

func trimData(data []byte) string {
	if len(data) > 16 {
		return fmt.Sprintf("D=%q +%d...", data[:16], len(data)-16)
	}
	return fmt.Sprintf("D=%q", data)
}

// Decode decodes data into a Chirp v0 error data payload.
func (e *ErrorData) Decode(data []byte) error {
	// Special case: An empty message is accepted as encoding empty details.
	if len(data) == 0 {
		*e = ErrorData{}
		return nil
	} else if len(data) < 4 {
		return fmt.Errorf("invalid error data (%d bytes)", len(data))
	}

	mlen := int(binary.BigEndian.Uint16(data[2:]))
	if 4+mlen > len(data) {
		return fmt.Errorf("error message truncated (%d > %d bytes)", 4+mlen, len(data))
	}
	msg := data[4 : 4+mlen]
	if !utf8.Valid(msg) {
		return errors.New("error message is not valid UTF-8")
	}
	e.Code = binary.BigEndian.Uint16(data[0:])
	e.Message = string(msg)
	e.Data = data[4+mlen:]
	if len(e.Data) == 0 {
		e.Data = nil
	}
	return nil
}
