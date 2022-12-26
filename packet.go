package chirp

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// Packet is the parsed format of a Chirp v0 packet.
type Packet struct {
	Protocol byte
	Type     PacketType
	Payload  []byte
}

// Encode encodes p in binary format.
func (p Packet) Encode() []byte {
	buf := bytes.NewBuffer(make([]byte, 0, 8+len(p.Payload)))
	if _, err := p.WriteTo(buf); err != nil {
		panic(fmt.Errorf("encoding packet: %w", err))
	}
	return buf.Bytes()
}

// WriteTo writes the packet to w in binary format. It satisfies io.WriterTo.
func (p *Packet) WriteTo(w io.Writer) (int64, error) {
	buf := [8]byte{'C', 'P', p.Protocol, byte(p.Type)}
	binary.BigEndian.PutUint32(buf[4:], uint32(len(p.Payload)))
	nw, err := w.Write(buf[:])
	if err == nil && len(p.Payload) != 0 {
		var np int
		np, err = w.Write(p.Payload)
		nw += np
	}
	return int64(nw), err
}

// ReadFrom reads a packet from r in binary format. It satisfies io.ReaderFrom.
func (p *Packet) ReadFrom(r io.Reader) (int64, error) {
	var buf [8]byte
	nr, err := io.ReadFull(r, buf[:])
	if err != nil {
		return int64(nr), fmt.Errorf("short packet header: %w", err)
	}
	if p := string(buf[:3]); p != "CP\x00" {
		return int64(nr), fmt.Errorf("invalid protocol version %q", p)
	}

	p.Protocol = buf[2]
	p.Type = PacketType(buf[3])

	if psize := binary.BigEndian.Uint32(buf[4:]); psize > 0 {
		p.Payload = make([]byte, int(psize))
		var np int
		np, err = io.ReadFull(r, p.Payload)
		nr += np
		if err != nil {
			err = fmt.Errorf("short payload: %w", err)
		}
	}

	return int64(nr), err
}

// String returns a human-friendly rendering of the packet.
func (p *Packet) String() string {
	var pay string
	switch p.Type {
	case PacketRequest:
		var req Request
		if err := req.UnmarshalBinary(p.Payload); err == nil {
			pay = req.String()
		}
	case PacketCancel:
		var can Cancel
		if err := can.UnmarshalBinary(p.Payload); err == nil {
			pay = can.String()
		}
	case PacketResponse:
		var rsp Response
		if err := rsp.UnmarshalBinary(p.Payload); err == nil {
			pay = rsp.String()
		}
	}
	if pay == "" {
		pay = fmt.Sprint(p.Payload)
	}
	return fmt.Sprintf("Packet(CP%v, %v, %s)", p.Protocol, p.Type, pay)
}

// PacketType describes the structure type of a Chirp v0 packet.
//
// All packet type values from 0 to 127 inclusive are reserved by the protocol
// and MUST NOT be used for any other purpose. Packet type values from 128-255
// are reserved for use by the implementation.
type PacketType byte

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
	MethodID  uint32
	Data      []byte
}

// Encode encodes the request data in binary format.
func (r Request) Encode() []byte {
	buf := make([]byte, 8+len(r.Data)) // 4 request ID, 4 method ID
	binary.BigEndian.PutUint32(buf[0:], r.RequestID)
	binary.BigEndian.PutUint32(buf[4:], r.MethodID)
	copy(buf[8:], r.Data)
	return buf
}

// UnmarshalBinary decodes data into a Chirp v0 request payload.
// It implements encoding.BinaryUnmarshaler.
func (r *Request) UnmarshalBinary(data []byte) error {
	if len(data) < 8 { // 4 request ID, 4 method ID
		return fmt.Errorf("short request payload (%d bytes)", len(data))
	}
	r.RequestID = binary.BigEndian.Uint32(data[0:])
	r.MethodID = binary.BigEndian.Uint32(data[4:])
	if len(data[8:]) > 0 {
		r.Data = data[8:]
	} else {
		r.Data = nil
	}
	return nil
}

// String returns a human-friendly rendering of the request.
func (r Request) String() string {
	return fmt.Sprintf("Request(ID=%v, Method=%v, Data=%+v)", r.RequestID, r.MethodID, r.Data)
}

// Response is the payload format for a Chirp v0 response packet.
type Response struct {
	RequestID uint32
	Code      ResultCode
	Data      []byte
}

// Encode encodes the response data in binary format.
func (r Response) Encode() []byte {
	buf := make([]byte, 5+len(r.Data)) // 4 request ID, 1 code
	binary.BigEndian.PutUint32(buf[0:], r.RequestID)
	buf[4] = byte(r.Code)
	copy(buf[5:], r.Data)
	return buf
}

// UnmarshalBinary decodes data into a Chirp v0 response payload.
// It implements encoding.BinaryUnmarshaler.
func (r *Response) UnmarshalBinary(data []byte) error {
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
		if ed.UnmarshalBinary(r.Data) == nil {
			data = fmt.Sprintf("ErrorData(Code=%d, [%d bytes], %q)", ed.Code, len(ed.Data), ed.Message)
		}
	}
	if data == "" {
		if len(r.Data) > 16 {
			data = fmt.Sprintf("Data=%+v ...", r.Data[:16])
		} else {
			data = fmt.Sprintf("Data=%+v", r.Data)
		}
	}
	return fmt.Sprintf("Response(ID=%v, Code=%v, %s)", r.RequestID, r.Code, data)
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
	binary.BigEndian.PutUint32(buf[:], c.RequestID)
	return buf[:]
}

// UnmarshalBinary decodes data into a Chirp v0 cancel payload.
// It impements encoding.BinaryUnmarshaler.
func (c *Cancel) UnmarshalBinary(data []byte) error {
	if len(data) != 4 {
		return fmt.Errorf("invalid cancel payload (%d bytes)", len(data))
	}
	c.RequestID = binary.BigEndian.Uint32(data)
	return nil
}

// String returns a human-friendly rendering of the cancellation.
func (c Cancel) String() string { return fmt.Sprintf("Cancel(ID=%v)", c.RequestID) }

// ErrorData is the response data format for a service error response.
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
	msg := truncate(e.Message, 65535)
	mlen := len(msg)

	buf := make([]byte, 4+mlen+len(e.Data)) // 2 code, 2 length
	binary.BigEndian.PutUint16(buf[0:], e.Code)
	binary.BigEndian.PutUint16(buf[2:], uint16(mlen))
	copy(buf[4:], msg)
	copy(buf[4+mlen:], e.Data)
	return buf
}

// truncate returns a prefix of a UTF-8 string s, having length no greater than
// n bytes.  If s exceeds this length, it is truncated at a point ≤ n so that
// the result does not end in a partial UTF-8 encoding.
func truncate(s string, n int) string {
	if n >= len(s) {
		return s
	}

	// Back up until we find the beginning of a UTF-8 encoding.
	for n > 0 && s[n-1]&0xc0 == 0x80 { // 0x10... is a continuation byte
		n--
	}

	// If we're at the beginning of a multi-byte encoding, back up one more to
	// skip it. It's possible the value was already complete, but it's simpler
	// if we only have to check in one direction.
	//
	// Otherwise, we have a single-byte code (0x00... or 0x01...).
	if n > 0 && s[n-1]&0xc0 == 0xc0 { // 0x11... starts a multibyte encoding
		n--
	}
	return s[:n]
}

// UnmarshalBinary decodes data into a Chirp v0 error data payload.
// It implements encoding.BinaryUnmarshaler.
func (e *ErrorData) UnmarshalBinary(data []byte) error {
	// Special case: An empty message is accepted as encoding empty details.
	if len(data) == 0 {
		*e = ErrorData{}
		return nil
	} else if len(data) < 4 {
		return fmt.Errorf("invalid error data (%d bytes)", len(data))
	}

	mlen := int(binary.BigEndian.Uint16(data[2:]))
	if 4+mlen > len(data) {
		return fmt.Errorf("error message truncated (%d > %d bytes)", 2+mlen, len(data))
	}
	e.Code = binary.BigEndian.Uint16(data[0:])
	e.Message = string(data[4 : 4+mlen])
	if d := data[4+mlen:]; len(data) != 0 {
		e.Data = d
	} else {
		e.Data = nil
	}
	return nil
}
