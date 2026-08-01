package ranges

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
)

func TestBuiltinIsValid(t *testing.T) {
	if err := validateRemote(Builtin()); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateAndNotModified(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{"success":true,"result":{"ipv4_cidrs":["173.245.48.0/20","103.21.244.0/22","103.22.200.0/22","103.31.4.0/22","141.101.64.0/18"],"ipv6_cidrs":["2606:4700::/32","2400:cb00::/32"]}}`))
	}))
	defer server.Close()
	cfg := config.Default().Ranges
	cfg.APIURL = server.URL
	cfg.RefreshInterval = 1
	m := NewCatalog(cfg, filepath.Join(t.TempDir(), "state"))
	first, err := m.Update(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Updated || first.Snapshot.ETag != `"v1"` {
		t.Fatalf("unexpected result: %#v", first)
	}
	time.Sleep(2 * time.Millisecond)
	second, err := m.Update(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if second.Updated || requests != 2 {
		t.Fatalf("not-modified handling failed: %#v, requests=%d", second, requests)
	}
}

func TestRejectsPrivateRange(t *testing.T) {
	s := Builtin()
	s.IPv4[0] = "10.0.0.0/8"
	if err := validateRemote(s); err == nil {
		t.Fatal("expected private range to be rejected")
	}
}
