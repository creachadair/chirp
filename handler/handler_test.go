// Copyright (C) 2023 Michael J. Fromberger. All Rights Reserved.

package handler_test

import (
	"context"
	"errors"
	"testing"

	"github.com/creachadair/chirp"
	"github.com/creachadair/chirp/handler"
	"github.com/creachadair/chirp/peers"
	"github.com/fortytw2/leaktest"
)

type tvText string

func (v tvText) MarshalText() ([]byte, error)     { return []byte(v), nil }
func (v *tvText) UnmarshalText(data []byte) error { *v = tvText(data); return nil }

type tvBinary string

func (v tvBinary) MarshalBinary() ([]byte, error)     { return []byte(v), nil }
func (v *tvBinary) UnmarshalBinary(data []byte) error { *v = tvBinary(data); return nil }

func TestHandler(t *testing.T) {
	defer leaktest.Check(t)()
	loc := peers.NewLocal()
	defer loc.Stop()

	check := func(t *testing.T, want, etext string, h chirp.Handler) {
		t.Helper()
		loc.A.Handle(0, h)
		ctx := context.Background()
		rsp, err := loc.B.Call(ctx, 0, []byte("input"))
		if err != nil {
			if got := err.Error(); got != etext {
				t.Fatalf("Call: got error %v, want %q", err, etext)
			}
		} else if etext != "" {
			t.Fatalf("Call: got %v, want error %q", rsp, etext)
		} else if got := string(rsp.Data); got != want {
			t.Errorf("Call result: got %q, want %q", got, want)
		}
	}
	checkReq := func(t *testing.T, ctx context.Context) {
		t.Helper()
		req := handler.ContextRequest(ctx)
		if req == nil {
			t.Error("Context does not contain request")
		}
	}

	t.Run("PRE", func(t *testing.T) {
		t.Run("StringString", func(t *testing.T) {
			check(t, "input-ok", "", handler.ParamResultError(
				func(ctx context.Context, s string) (string, error) {
					checkReq(t, ctx)
					return s + "-ok", nil
				},
			))
		})
		t.Run("StringByte", func(t *testing.T) {
			check(t, "input-ok", "", handler.ParamResultError(
				func(ctx context.Context, s string) ([]byte, error) {
					checkReq(t, ctx)
					return []byte(s + "-ok"), nil
				},
			))
		})
		t.Run("TextByte", func(t *testing.T) {
			check(t, "input-ok", "", handler.ParamResultError(
				func(ctx context.Context, s tvText) ([]byte, error) {
					checkReq(t, ctx)
					return []byte(s + "-ok"), nil
				},
			))
		})
		t.Run("BinaryText", func(t *testing.T) {
			check(t, "input-ok", "", handler.ParamResultError(
				func(ctx context.Context, s tvBinary) (tvText, error) {
					checkReq(t, ctx)
					return tvText(s + "-ok"), nil
				},
			))
		})
		t.Run("Error", func(t *testing.T) {
			check(t, "", "service error: bad robot", handler.ParamResultError(
				func(ctx context.Context, s string) (string, error) {
					checkReq(t, ctx)
					return "", errors.New("bad robot")
				},
			))
		})
	})

	t.Run("PR", func(t *testing.T) {
		t.Run("StringString", func(t *testing.T) {
			check(t, "input-ok", "", handler.ParamResult(
				func(ctx context.Context, s string) string { checkReq(t, ctx); return s + "-ok" },
			))
		})
		t.Run("StringByte", func(t *testing.T) {
			check(t, "input-ok", "", handler.ParamResult(
				func(ctx context.Context, s string) []byte { checkReq(t, ctx); return []byte(s + "-ok") },
			))
		})
		t.Run("TextByte", func(t *testing.T) {
			check(t, "input-ok", "", handler.ParamResult(
				func(ctx context.Context, s tvText) []byte { checkReq(t, ctx); return []byte(s + "-ok") },
			))
		})
		t.Run("BinaryText", func(t *testing.T) {
			check(t, "input-ok", "", handler.ParamResult(
				func(ctx context.Context, s tvBinary) tvText { checkReq(t, ctx); return tvText(s + "-ok") },
			))
		})
	})

	t.Run("PE", func(t *testing.T) {
		t.Run("String", func(t *testing.T) {
			check(t, "", "service error: ok", handler.ParamError(
				func(ctx context.Context, s string) error { checkReq(t, ctx); return errors.New("ok") },
			))
		})
		t.Run("Byte", func(t *testing.T) {
			check(t, "", "service error: ok", handler.ParamError(
				func(ctx context.Context, b []byte) error { checkReq(t, ctx); return errors.New("ok") },
			))
		})
		t.Run("Text", func(t *testing.T) {
			check(t, "", "service error: ok", handler.ParamError(
				func(ctx context.Context, s tvText) error {
					checkReq(t, ctx)
					return chirp.ErrorData{Message: "ok", Data: []byte("hi")}
				},
			))
		})
		t.Run("Binary", func(t *testing.T) {
			check(t, "", "service error: [code 100] ok", handler.ParamError(
				func(ctx context.Context, s tvBinary) error {
					checkReq(t, ctx)
					return chirp.ErrorData{Code: 100, Message: "ok"}
				},
			))
		})
	})

	t.Run("RE", func(t *testing.T) {
		t.Run("String", func(t *testing.T) {
			check(t, "please", "", handler.ResultError(
				func(ctx context.Context) (string, error) {
					checkReq(t, ctx)
					return "please", nil
				},
			))
		})
		t.Run("Byte", func(t *testing.T) {
			check(t, "clap", "", handler.ResultError(
				func(ctx context.Context) ([]byte, error) {
					checkReq(t, ctx)
					return []byte("clap"), nil
				},
			))
		})
		t.Run("Text", func(t *testing.T) {
			check(t, "", "service error: ok", handler.ResultError(
				func(ctx context.Context) (tvText, error) {
					checkReq(t, ctx)
					return "", chirp.ErrorData{Message: "ok", Data: []byte("hi")}
				},
			))
		})
		t.Run("Binary", func(t *testing.T) {
			check(t, "louder", "", handler.ResultError(
				func(ctx context.Context) (tvBinary, error) {
					checkReq(t, ctx)
					return "louder", nil
				},
			))
		})
	})

	t.Run("RO", func(t *testing.T) {
		t.Run("String", func(t *testing.T) {
			check(t, "please", "", handler.ResultOnly(
				func(ctx context.Context) string { checkReq(t, ctx); return "please" },
			))
		})
		t.Run("Byte", func(t *testing.T) {
			check(t, "clap", "", handler.ResultOnly(
				func(ctx context.Context) []byte { checkReq(t, ctx); return []byte("clap") },
			))
		})
		t.Run("Text", func(t *testing.T) {
			check(t, "more", "", handler.ResultOnly(
				func(ctx context.Context) tvText { checkReq(t, ctx); return "more" },
			))
		})
		t.Run("Binary", func(t *testing.T) {
			check(t, "loudly", "", handler.ResultOnly(
				func(ctx context.Context) tvBinary { checkReq(t, ctx); return "loudly" },
			))
		})
	})
}
