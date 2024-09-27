# chirp

[![GoDoc](https://img.shields.io/static/v1?label=godoc&message=reference&color=mistyrose)](https://pkg.go.dev/github.com/creachadair/chirp)
[![CI](https://github.com/creachadair/chirp/actions/workflows/go-presubmit.yml/badge.svg?event=push&branch=main)](https://github.com/creachadair/chirp/actions/workflows/go-presubmit.yml)

This repository defines Chirp, a lightweight remote procedure call protocol
suitable for use over stream-oriented transports like sockets and pipes. The
packet format is byte-oriented and uses fixed header formats to minimize the
amount of bit-level manipulation necessary to encode and decode packets.

The specification and its implementation are still in development and should
not be considered ready for production use.

- [Specification](spec.md)
- [Go implementation](https://godoc.org/github.com/creachadair/chirp)
