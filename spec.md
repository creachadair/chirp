# Chirp v0 Specification

Chirp is a lightweight remote procedure call protocol. Peers exchange binary packets over a shared channel, following the packet format described below. The protocol is intended to be easy to implement in a variety of different languages and to perform efficiently over bidirectional stream-oriented local transport mechanisms such as sockets and pipes. The packet format is byte-oriented and uses fixed header formats to minimize the amount of bit-level manipulation necessary to encode and decode packets.

This document uses key words as described in [RFC 2119](https://datatracker.ietf.org/doc/html/rfc2119).

- [Packet format](#packet-format)
- [Packet types](#packet-type)
- Payloads:
    - [Request](#request-payload)
    - [Response](#response-payload)
        - [Result codes](#result-codes)
    - [Cancel](#cancel-payload)
    - [Error](#error-data)
- [Protocol definition](#protocol-definition)
    - [Session processing](#session-processing)
    - [Error handling](#error-handling)
        - [Protocol fatal](#protocol-fatal-conditions)
        - [Silent discard](#silent-discard-conditions)
        - [Error response](#error-response-conditions)
    - [Call subprotocol](#call-subprotocol)
        - [Cancellation](#cancellation)
    - [Custom subprotocols](#custom-subprotocols)

## Packet Format

A packet is an array of bytes with the following structure:

| Offset | Bytes | Description                     |
|--------|-------|---------------------------------|
| 0      | 3     | "CP\x00" [67, 80, 0]            |
| 3      | 1     | packet type                     |
| 4      | 4     | size of payload (BE uint32 = n) |
| 8      | n     | payload                         |

A minimal packet is 8 bytes, consisting of the magic number, packet type, and 4-byte payload size with an empty payload.

**Implementation note:** A packet is designed to be self-framing, in that once the header is read the exact length of the payload is known and can be consumed. This permits packets to be sent and received over unframed binary streams such as pipes and sockets, or packed into files.

### Packet Type

All packet type values from 0 to 127 inclusive are reserved by the protocol and MUST NOT be used for any other purpose. Packet type values from 128???255 are reserved for use by the implementation.

| Value   | Description                   | Payload format                |
|---------|-------------------------------|-------------------------------|
| 0-1     | (reserved by protocol)        |                               |
| 2       | Request                       | [Request](#request-payload)   |
| 3       | Cancel request                | [Cancel](#cancel-payload)     |
| 4       | Response                      | [Response](#response-payload) |
| 5-127   | (reserved by protocol)        |                               |
| 128-255 | (reserved for implementation) | implementation-defined        |

### Request Payload

The payload of a Request packet has the following structure:

| Offset | Bytes | Description            |
|--------|-------|------------------------|
| 0      | 4     | Request ID (BE uint32) |
| 4      | 4     | Method ID (BE uint32)  |
| 4      | rest  | Parameter data         |

- The **Request ID** is an identifier assigned by the caller. It must be unique among pending requests from that caller, but the caller is otherwise free to reuse request IDs for requests that are not concurrent.

- The **Method ID** is a method identifier defined by the callee, opaque to the protocol.

- The **Parameter data** are an uninterpreted sequence of bytes (empty OK).

### Response Payload

The payload of a Response packet has the following structure:

| Offset | Bytes | Description                  |
|--------|-------|------------------------------|
| 0      | 4     | Request ID (BE uint32)       |
| 4      | 1     | [Result code](#result-codes) |
| 5      | rest  | Response data                |

- The **Request ID** identifies which request this response belongs to.

- The **Result code** indicates whether the response was successful, and if it was not indicates the reason for failure.

- The **Response data** contain a result from the method handler or an indication of error depending on the [result code](#result-codes).

#### Result Codes

All result codes not defined here are reserved for future use by the protocol.

| Value | Description       | Response data                               |
|-------|-------------------|---------------------------------------------|
| 0     | Success           | uninterpreted bytes (method handler result) |
| 1     | Unknown method    | empty                                       |
| 2     | Duplicate request | empty                                       |
| 3     | Request canceled  | empty                                       |
| 4     | Service error     | [Error](#error-data)                        |
| 5-255 | (reserved)        |                                             |

A request **succeeds** if its request ID is not a duplicate, the method ID is known by the callee, and the callee's method handler completes without error. On a successful request, the response data are the uninterpreted result returned from the method handler.

A **service error** occurs when a method handler fails to complete normally (for example, as a result of a panic or exception), or otherwise reports an error without producing a result. In this case, the implementation MUST set the result code to 4 (Service error) and the response data to an [Error](#error-data).

### Error Data

The response data in case of a service error uses the following structure:

| Offset | Bytes | Description                        |
|--------|-------|------------------------------------|
| 0      | 2     | Error code (BE uint16)             |
| 2      | 2     | Description length (BE uint16 = m) |
| 4      | m     | Description (UTF-8 text)           |
| 4+m    | rest  | Auxuiliary data                    |

- The **Error code** is an uninterpreted machine-readable error code describing the meaning of the error. The implementation SHOULD permit the method handler to choose this value; otherwise the implementation SHOULD set this field to 0.

- The **Description** is a length-prefixed string giving a human-readable description of the error. This field MAY be empty but if non-empty MUST be encoded in UTF-8. The description MUST NOT exceed 65535 bytes in length; the implementation should truncate the message as necessary to fit within this constraint.

- The **Auxiliary data** are an uninterpreted sequence of bytes chosen by the handler (empty OK). This field can be used to pass application-specific structured error information back to the caller.

As a special case, then implementation SHALL treat an empty byte array as a valid encoding for error data with error code 0, an empty description, and empty auxiliary data.

### Cancel Payload

The payload of a Cancel packet has the following structure:

| Offset | Bytes | Description            |
|--------|-------|------------------------|
| 0      | 4     | Request ID (BE uint32) |

- The **Request ID** identifies which pending request to cancel.


## Protocol Definition

The current protocol is Version 0, indicated by the packet header `CP\x00`.

### Session Processing

A Chirp protocol session begins by establishing a reliable, bidirectional, ordered channel carrying packets between a pair of peers.

- **Reliable** means the channel must either deliver each packet sent, or must be marked closed and report an error. A closed channel is invalid and must report an error for any subsequent use.
- **Bidirectional** means packets can be sent and received on the channel concurrently. Sending MUST NOT block receiving and vice versa.
- **Ordered** means packets sent by either peer must be delivered to the other peer in the order they were sent.
- Each packet is transmitted whole, there are no fragments.

A session continues until the channel fails or is explicitly closed by either peer. When the channel terminates, all pending inbound calls SHOULD be interrupted, and whether interrupted or not the results of those calls MUST be discarded. All pending outbound calls MUST fail and report errors. The implementation SHOULD log or report a diagnostic to the host and MUST report an error for any subsequent attempt to use the session.

While a session is active, each peer processes the packets sent by the other peer on the channel in order, according to the rules defined below. Either in response to remote peer requests or on behalf of the host, the peer also sends packets to the remote peer.

### Error Handling

Basic errors in the protocol implementation are **protocol fatal**. For a protocol fatal error, the peer MUST immediately terminate the channel and report or log an error to the host. Protocol fatal errors are the result of channel failures, resource exhaustion, or an incorrect peer implementation rather than host code.

To **silently discard** a packet means that the receiving peer MUST fully consume the packet and MUST NOT send a response to the sending peer. The channel is not closed. The receiving peer is free to log or otherwise report the packet to the host.

To **respond with error** means that the receiving peer MUST fully consume the packet and send a response to the sending peer indicating the error condition. The channel is not closed.

#### Protocol Fatal Conditions

A peer MUST **protocol fatal** for:

- Receiving a short or invalid packet header.
- Receiving a valid packet header with a short payload.
- Receiving a valid packet of known type but an invalid payload.
- A channel failure while sending a packet.

**Implementation note:** An invalid payload may be structurally valid but contain invalid data, for example a Response payload with an unknown result code. For an implementation-defined (custom) packet type, the validity of the payload determined by the implementation. The implementation MAY respond to an invalid custom payload with an error, but otherwise MUST treat an invalid payload as protocol fatal.

#### Silent Discard Conditions

A peer MUST **silently discard**:

- A packet with an unrecognized packet type.
- A Response packet with a completed or unknown request ID.
- A Cancel packet with a completed or unknown request ID.

#### Error Response Conditions

A peer MUST **respond with error** for:

- A Request packet with an unknown method ID (code 1).
- A Request packet with a duplicated pending request ID (code 2).


### Call Subprotocol

A **call** is a directional exchange between the two peers, consisting of a **request** and a corresponding **response**. This is the primary communication mechanism between peers, and a compliant peer MUST support the call subprotocol.  The peer that initiates the call is the **caller**, the peer that responds is the **callee**. Calls may propagate in either direction.

The sequence of operations for a call is:

1. The caller sends a `Request(id, method, params)` packet to the callee. At this point the call is *pending*. The call remains pending until either *terminated* or *completed* according to the rules below.

   - If `id` duplicates an already pending request, the callee MUST send a `Response(id, DUPLICATE_REQUEST, nil)` packet. This terminates the call. The callee MUST NOT interrupt or terminate the already-pending request as a result of the duplication.

   - If `method` is unknown, the callee MUST send a `Response(id, UNKNOWN_METHOD, nil)` packet. This terminates the call.
2. The callee runs the handler for the requested method.

   - If the handler completes with result `R`, the callee sends `Response(id, SUCCESS, R)`. This completes the call.

     **Implementation note:** A successful call may still report an application-specific error to the caller as a variant within the successful result.

   - If the handler terminates abnormally (for example via an exception or a panic), the callee sends `Response(id, SERVICE_ERROR, E)` where `E` is either empty or `Error(C, desc, data)` for an implementation-defined choice of code `C`, human readable description message `desc`, and ancillary data `data`.  This completes the call.

   - If the handler reports an error instead of a result, the callee sends `Response(id, SERVICE_ERROR, E)` where `E` is either empty or `Error(C, desc, data)` for a handler-defined choice of code `C`, human-readable description message `desc`, and ancillary data `data`.  This completes the call.

Once a call is either terminated or complete, the `id` value for that call is eligible for reuse.

**Implementation note:** The rules above define the order of operations for a single call, but a call is not required to terminate or complete before another call (with a different request ID) can be initiated. Peers may initiate multiple calls concurrently, provided the request IDs are distinct.  The request IDs for inbound and outbound calls are independent, and may overlap, for example, peer A may send a request with ID 1 at the same time as peer B sends a request with ID 1. These are distinct requests, not duplicates.

#### Cancellation

While a call `id` is *pending*, the caller may request its cancellation. To do so, the caller sends a `Cancel(id)` packet to the callee:

- If `id` is unknown or has already completed, the callee MUST silently discard the packet.

- Otherwise: If the call has not yet been dispatched to a handler, the callee MUST discard it and send `Response(id, CANCELED, nil)`. This terminates the call for request `id`.

- Otherwise: If the handler is running, the callee SHOULD attempt to *interrupt* the execution of the handler.

  - If interruption is successful, the callee sends `Response(id, CANCELED, nil)`. This terminates the call for request `id`.

  - If interruption is not possible, the callee MAY send `Response(id, CANCELED, nil)` immediately and discard the handler result when it completes. This terminates the call for request `id`.

  - Otherwise, the callee MUST ignore the cancellation request and allow the handler to complete normally.

- Otherwise: If the call handler has already completed, the handler SHOULD report its result as a normal response, completing the call. Alternatively, the callee MAY discard the result and send `Response(id, CANCELED, nil)` instead. This terminates the call.

If cancellation succeeds, the cancellation response supersedes a handler response. Whether or not cancellation succeeds, the callee MUST NOT send multiple responses for the same request.

**Implementation note:** The `Cancel(id)` packet is not itself a request and does not require its own reply.

### Custom Subprotocols

Packet type values from 128-255 are reserved for use by the implementation. A peer implementation is permitted to send and accept packets with types in this custom range to define other subprotocols. Apart from the basic packet structure, the semantics of custom packet types are entirely up to the implementation.

Because a peer that does not recognize the type of a structurally valid packet is required to ignore the packet, peers may need to advertise or negotiate capabilities for custom subprotocols.  The default [call subprotocol](#call) should be used for this purpose.
