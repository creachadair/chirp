# chirp

[![GoDoc](https://img.shields.io/static/v1?label=godoc&message=reference&color=mistyrose)](https://pkg.go.dev/github.com/creachadair/chirp)
[![CI](https://github.com/creachadair/chirp/actions/workflows/go-presubmit.yml/badge.svg?event=push&branch=main)](https://github.com/creachadair/chirp/actions/workflows/go-presubmit.yml)

This repository defines Chirp, a lightweight remote procedure call protocol
suitable for use over stream-oriented transports like sockets and pipes. It
uses a byte-oriented packet format with fixed headers to minimize the
amount of bit-level manipulation necessary to encode and decode messages.

The specification and its implementation are still in development at v0. Tagged
versions will increment the minor revision for substantial new features and
breaking changes, and the patch revision for bug fixes and other compatible
updates.

- [Specification](spec.md)
- [Go implementation](https://godoc.org/github.com/creachadair/chirp)
- [Usage example in the Go Playground](https://go.dev/play/p/mnxMmlucqZu)
