// Copyright (C) 2023 Michael J. Fromberger. All Rights Reserved.

package catalog_test

import (
	"context"
	"testing"

	"github.com/creachadair/chirp"
	"github.com/creachadair/chirp/catalog"
	"github.com/creachadair/chirp/peers"
	"github.com/creachadair/mtest"
	"github.com/google/go-cmp/cmp"
)

func TestCatalogUsage(t *testing.T) {
	cat := catalog.New().Set("test0", 0).Set("test1", 100)

	loc := peers.NewLocal()
	loc.A.LogPackets(func(pkt chirp.PacketInfo) { t.Logf("A: %v", pkt) })
	defer loc.Stop()

	// The Peer method should return the bound peer.
	ca := cat.Bind(loc.A)
	if got := ca.Peer(); got != loc.A {
		t.Errorf("ca.Peer: got %v, want %v", got, loc.A)
	}
	cb := cat.Bind(loc.B)
	if got := cb.Peer(); got != loc.B {
		t.Errorf("ca.Peer: got %v, want %v", got, loc.B)
	}

	// The original catalog does not have a peer.
	if got := cat.Peer(); got != nil {
		t.Errorf("cat.Peer: got %v, want nil", got)
	}
	ctx := context.Background()

	ca.
		Handle("test0", func(ctx context.Context, req *chirp.Request) ([]byte, error) {
			return []byte("default"), nil
		}).
		Handle("test1", func(ctx context.Context, req *chirp.Request) ([]byte, error) {
			return []byte("one"), nil
		})

	t.Run("HandleUnknown", func(t *testing.T) {
		mtest.MustPanic(t, func() { ca.Handle("nonesuch", nil) })
	})

	checkCall := func(t *testing.T, name, want string) {
		t.Helper()
		rsp, err := cb.Call(ctx, name, nil)
		if err != nil {
			t.Fatalf("Call %q unexpectedly failed: %v", name, err)
		} else if got := string(rsp.Data); got != want {
			t.Fatalf("Call %q: got %q, want %q", name, got, want)
		}
	}

	t.Run("Call0_B", func(t *testing.T) { checkCall(t, "test0", "default") })
	t.Run("Call1_B", func(t *testing.T) { checkCall(t, "test1", "one") })
	t.Run("Call2_B", func(t *testing.T) { checkCall(t, "test2", "default") }) // fall through to default
	t.Run("CallUnknown_B", func(t *testing.T) { checkCall(t, "nonesuch", "default") })

	// Add a new binding to the catalog and exercise it.
	cat.Set("test2", 935)
	ca.Handle("test2", func(ctx context.Context, req *chirp.Request) ([]byte, error) {
		return []byte("two"), nil
	})
	t.Run("Call2_B_Defined", func(t *testing.T) { checkCall(t, "test2", "two") })

	t.Run("CallUnknown_A", func(t *testing.T) {
		if rsp, err := ca.Call(ctx, "nonesuch", nil); err == nil {
			t.Errorf("Call nonesuch: got %q, want error", rsp)
		}
	})

	checkExec := func(t *testing.T, name, want string) {
		t.Helper()
		data, err := ca.Exec(ctx, name, &chirp.Request{RequestID: 1, MethodID: 999})
		if err != nil {
			t.Fatalf("Exec %q unexpectedly failed: %v", name, err)
		} else if got := string(data); got != want {
			t.Fatalf("Exec %q: got %q, want %q", name, got, want)
		}
	}

	t.Run("Exec0_A", func(t *testing.T) { checkExec(t, "test0", "default") })
	t.Run("Exec1_A", func(t *testing.T) { checkExec(t, "test1", "one") })
	t.Run("ExecUnknown_A", func(t *testing.T) { checkExec(t, "nonesuch", "default") })

	t.Run("ExecUnknown_B", func(t *testing.T) {
		if data, err := cb.Exec(ctx, "nonesuch", &chirp.Request{RequestID: 1, MethodID: 999}); err == nil {
			t.Errorf("Exec nonesuch: got %q, want error", data)
		}
	})
}

func TestCatalogEncoding(t *testing.T) {
	initCat := func() catalog.Catalog {
		return catalog.New().
			Set("minsc", 101).
			Set("boo", 102).
			Set("dynaheir", 100987).
			Set("viconia", 666)
	}
	checkEqual := func(t *testing.T, got, want catalog.Catalog) {
		t.Helper()
		if diff := cmp.Diff(got, want, cmp.AllowUnexported(catalog.Catalog{})); diff != "" {
			t.Fatalf("Catalog: (-got, +want):\n%s", diff)
		}
	}

	t.Run("Lookup", func(t *testing.T) {
		want := map[string]uint32{"minsc": 101, "boo": 102, "nonesuch": 0}
		cat := initCat()

		for name, id := range want {
			if got := cat.Lookup(name); got != id {
				t.Errorf("Lookup %q: got %d, want %d", name, got, id)
			}
		}
	})

	t.Run("RoundTrip", func(t *testing.T) {
		want := initCat()
		enc := want.Encode()
		t.Logf("Encoded catalog: %q", enc)
		var got catalog.Catalog
		if err := got.Decode(enc); err != nil {
			t.Fatalf("Decode catalog: unexpected error: %v", err)
		}
		checkEqual(t, got, want)
	})

	t.Run("Handler", func(t *testing.T) {
		loc := peers.NewLocal()
		loc.A.LogPackets(func(pkt chirp.PacketInfo) { t.Logf("A: %v", pkt) })
		defer loc.Stop()

		// Set up a catalog with a method to query the catalog itself.
		cat := initCat().Add("catalog")

		// Bind the handler for that method on A.
		cat.Bind(loc.A).Handle("catalog", cat.Handler)

		// Call the catalog method from B.
		rsp, err := cat.Bind(loc.B).Call(context.Background(), "catalog", nil)
		if err != nil {
			t.Fatalf("Call: unexpected error: %v", err)
		}

		// Make sure we got the same set back.
		var got catalog.Catalog
		if err := got.Decode(rsp.Data); err != nil {
			t.Fatalf("Decode response: unexpected error: %v", err)
		}
		checkEqual(t, got, cat)
	})
}
