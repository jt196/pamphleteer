package main

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

var mtimeClock atomic.Int64

// writeVault writes a file and gives it a strictly increasing mtime, so the
// scanner's (mtime,size) cache can never mistake an edit for "unchanged".
func writeVault(t *testing.T, root, rel, content string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := time.Unix(1_700_000_000+mtimeClock.Add(10), 0)
	if err := os.Chtimes(p, ts, ts); err != nil {
		t.Fatal(err)
	}
	return p
}

// pubNoteSrc builds a published note with the given slug and body.
func pubNoteSrc(slug, body string) string {
	return "---\npublish: true\nslug: " + slug + "\n---\n" + body
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newTest(t *testing.T, files map[string]string) (*app, *scanner, string) {
	t.Helper()
	root := t.TempDir()
	for rel, c := range files {
		writeVault(t, root, rel, c)
	}
	a := newApp()
	sc := newScanner(root, testLogger(), &a.snap)
	if _, err := sc.scan(); err != nil {
		t.Fatal(err)
	}
	return a, sc, root
}

func get(a *app, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func body(rec *httptest.ResponseRecorder) string { return rec.Body.String() }

func mustStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d (body: %.200s)", rec.Code, want, body(rec))
	}
}

func servedSlugs(a *app) map[string]bool {
	out := map[string]bool{}
	if s := a.snap.Load(); s != nil {
		for k := range s.pages {
			out[k] = true
		}
	}
	return out
}
