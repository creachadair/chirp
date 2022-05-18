package chirp

import (
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

// WriteTo writes the packet to w in binary format. It satisfies io.WriterTo.
func (p *Packet) WriteTo(w io.Writer) (int64, error) {
	var buf [8]byte
	buf[0] = 'C'
	buf[1] = 'P'
	buf[2] = p.Protocol
	buf[3] = byte(p.Type)
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
		return "request"
	case PacketCancel:
		return "cancel"
	case PacketResponse:
		return "response"
	default:
		return fmt.Sprintf("packet type %d", byte(p))
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
	if len(data[5:]) > 0 {
		r.Data = data[5:]
	} else {
		r.Data = nil
	}
	return nil
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
	mlen := len(e.Message)
	buf := make([]byte, 4+mlen+len(e.Data)) // 2 code, 2 length
	binary.BigEndian.PutUint16(buf[0:], e.Code)
	binary.BigEndian.PutUint16(buf[2:], uint16(mlen))
	copy(buf[4:], e.Message)
	copy(buf[4+mlen:], e.Data)
	return buf
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
	if 2+mlen > len(data) {
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
