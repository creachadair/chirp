// Copyright (C) 2023 Michael J. Fromberger. All Rights Reserved.

// Package handler provides adapters to the chirp.Handler type for functions
// with other signatures.
//
// Parameters may be []byte or string, or a type whose pointer supports one of
// the encoding.BinaryUnmarshaler or encoding.TextUnmarshaler interfaces.
//
// Results may be []byte or string, or any type that supports the one of the
// encoding.BinaryMarshaler or encoding.TextMarshaler interfaces.
package handler

import (
	"bytes"
	"context"
	"encoding"
	"fmt"

	"github.com/creachadair/chirp"
)

// reqContextKey is a context key for the request value to a handler.
type reqContextKey struct{}

// ContextRequest returns the original request message passed to the handler,
// or nil if ctx has no associated request.  The context passed to a handler
// returned by this package will have this valuep.
func ContextRequest(ctx context.Context) *chirp.Request {
	if v := ctx.Value(reqContextKey{}); v != nil {
		return v.(*chirp.Request)
	}
	return nil
}

// ParamResultError adapts a function f that accepts parameters of type P and
// returns a result of type R and an error, to a chirp.Handler.
func ParamResultError[P, R any](f func(context.Context, P) (R, error)) chirp.Handler {
	return func(ctx context.Context, req *chirp.Request) ([]byte, error) {
		var p P
		if err := unmarshal(req.Data, &p); err != nil {
			return nil, err
		}
		hctx := context.WithValue(ctx, reqContextKey{}, req)
		r, err := f(hctx, p)
		if err != nil {
			return nil, err
		}
		return marshal(r)
	}
}

// ParamResult adapts a function f that accepts parameters of type P and
// returns a result of type R without error, to a chirp.Handler.
func ParamResult[P, R any](f func(context.Context, P) R) chirp.Handler {
	return func(ctx context.Context, req *chirp.Request) ([]byte, error) {
		var p P
		if err := unmarshal(req.Data, &p); err != nil {
			return nil, err
		}
		hctx := context.WithValue(ctx, reqContextKey{}, req)
		return marshal(f(hctx, p))
	}
}

// ParamError adapts a function f that accepts parameters of type P and returns
// an error with no result, to a chirp.Handler.
func ParamError[P any](f func(context.Context, P) error) chirp.Handler {
	return func(ctx context.Context, req *chirp.Request) ([]byte, error) {
		var p P
		if err := unmarshal(req.Data, &p); err != nil {
			return nil, err
		}
		hctx := context.WithValue(ctx, reqContextKey{}, req)
		return nil, f(hctx, p)
	}
}

// ResultError adapts a function f that accepts no parameters and returns a
// result of type R and an error, to a chirp.Handler.
func ResultError[R any](f func(context.Context) (R, error)) chirp.Handler {
	return func(ctx context.Context, req *chirp.Request) ([]byte, error) {
		hctx := context.WithValue(ctx, reqContextKey{}, req)
		r, err := f(hctx)
		if err != nil {
			return nil, err
		}
		return marshal(r)
	}
}

// unmarshal decodes data into v. The concrete type of v must be a pointer to a
// []byte or string, or must implement either the encoding.BinaryUnmarshaler
// interface or the encoding.TextUnmarshaler interface.  If v implements both,
// BinaryUnmarshaler is preferred.
func unmarshal(data []byte, v any) error {
	switch t := v.(type) {
	case *[]byte:
		*t = bytes.Clone(data)
	case *string:
		*t = string(data)
	case encoding.BinaryUnmarshaler:
		return t.UnmarshalBinary(data)
	case encoding.TextUnmarshaler:
		return t.UnmarshalText(data)
	default:
		return fmt.Errorf("cannot unmarshal into %T", v)
	}
	return nil
}

// marshal encodes v into data. The concrete type of v must be a []byte or
// string (or a pointer to these); otherwise it must implement either the
// encoding.BinaryMarshaler interface or the encoding.TextMarshaler
// interface. If v implements both, BinaryUnmarshaler is preferred.
//
// As a special case if v is a nil pointer to a string or []byte, the result is
// nil without error.
func marshal(v any) ([]byte, error) {
	switch t := v.(type) {
	case []byte:
		return t, nil
	case *[]byte:
		if t == nil {
			return nil, nil
		}
		return *t, nil
	case string:
		return []byte(t), nil
	case *string:
		if t == nil {
			return nil, nil
		}
		return []byte(*t), nil
	case encoding.BinaryMarshaler:
		return t.MarshalBinary()
	case encoding.TextMarshaler:
		return t.MarshalText()
	default:
		return nil, fmt.Errorf("cannot marshal %T", v)
	}
}
