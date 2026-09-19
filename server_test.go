package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func osSymlink(oldname, newname string) error { return os.Symlink(oldname, newname) }

func TestPublishedPageHeaders(t *testing.T) {
	a, _, _ := newTest(t, map[string]string{"n.md": pubNoteSrc("page12345", "hello")})
	rec := get(a, "GET", "/page12345")
	mustStatus(t, rec, 200)
	h := rec.Header()
	want := map[string]string{
		"Content-Type":           "text/html; charset=utf-8",
		"Cache-Control":          "no-cache",
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		"X-Robots-Tag":           "noindex, nofollow, noarchive",
	}
	for k, v := range want {
		if h.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, h.Get(k), v)
		}
	}
	csp := h.Get("Content-Security-Policy")
	for _, must := range []string{"default-src 'none'", "frame-ancestors 'none'", "style-src 'self'"} {
		if !strings.Contains(csp, must) {
			t.Errorf("CSP %q missing %q", csp, must)
		}
	}
	if strings.Contains(csp, "script-src") || strings.Contains(csp, "unsafe") {
		t.Errorf("CSP must not allow scripts or unsafe sources: %q", csp)
	}
}

func TestConditionalGetAndHead(t *testing.T) {
	a, _, _ := newTest(t, map[string]string{"n.md": pubNoteSrc("page12345", "hello")})
	rec := get(a, "GET", "/page12345")
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	req := httptest.NewRequest("GET", "/page12345", nil)
	req.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	a.ServeHTTP(rec2, req)
	mustStatus(t, rec2, 304)

	head := get(a, "HEAD", "/page12345")
	mustStatus(t, head, 200)
	if head.Body.Len() != 0 {
		t.Fatal("HEAD must not return a body")
	}
}

func TestNotFoundIsUniformAndUncacheable(t *testing.T) {
	a, _, _ := newTest(t, map[string]string{
		"n.md":     pubNoteSrc("page12345", "hello"),
		"draft.md": "---\npublish: false\nslug: draft12345\n---\nx",
	})
	var first string
	for i, p := range []string{"/", "/nope", "/draft12345", "/draft", "/n", "/n.md", "/page12345/", "/page12345/extra", "/PAGE12345", "/probe-marker-xyz"} {
		rec := get(a, "GET", p)
		mustStatus(t, rec, 404)
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: 404 must be no-store", p)
		}
		if i == 0 {
			first = body(rec)
		} else if body(rec) != first {
			t.Errorf("%s: 404 body differs, which would reveal existence", p)
		}
		if strings.Contains(body(rec), "probe-marker-xyz") {
			t.Errorf("%s: 404 page must not echo the requested path", p)
		}
	}
	// It is a real page that reuses the site stylesheet, and stays script-free.
	mustContain(t, first, `<link rel="stylesheet" href="/style.css">`, "Not found", `noindex`)
	mustNotContain(t, first, "<script")
}

func TestPathTraversalCannotReachTheFilesystem(t *testing.T) {
	a, _, _ := newTest(t, map[string]string{
		"n.md":        pubNoteSrc("page12345", "hello"),
		"Main.md":     "---\npublish: false\n---\nprivate",
		"img/pic.png": "png",
		"Secret/x.md": "private",
	})
	paths := []string{
		"/../etc/passwd", "/%2e%2e/etc/passwd", "/..%2f..%2fetc%2fpasswd", "//etc/passwd",
		"/page12345/../Main.md", "/./page12345", "/Main.md", "/Secret/x.md", "/img/pic.png",
		"/.obsidian/app.json", "/page12345%00.png", "/%00",
	}
	for _, p := range paths {
		rec := httptest.NewRecorder()
		req, err := http.NewRequest("GET", "http://x"+p, nil)
		if err != nil {
			continue // unparseable paths never reach a handler
		}
		a.ServeHTTP(rec, req)
		if rec.Code == 200 && !strings.Contains(p, "page12345") {
			t.Errorf("%s served with 200", p)
		}
		if strings.Contains(body(rec), "private") || strings.Contains(body(rec), "root:") {
			t.Errorf("%s leaked content: %.100s", p, body(rec))
		}
	}
}

func TestOnlyGetAndHead(t *testing.T) {
	a, _, _ := newTest(t, map[string]string{"n.md": pubNoteSrc("page12345", "hello")})
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH", "OPTIONS"} {
		rec := get(a, m, "/page12345")
		mustStatus(t, rec, 405)
		if rec.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s: Allow = %q", m, rec.Header().Get("Allow"))
		}
	}
}

func TestFixedRoutes(t *testing.T) {
	a, _, _ := newTest(t, nil)
	mustStatus(t, get(a, "GET", "/healthz"), 200)
	if r := get(a, "GET", "/robots.txt"); !strings.Contains(body(r), "Disallow: /") {
		t.Fatal("robots.txt should disallow everything")
	}
	mustStatus(t, get(a, "GET", "/favicon.ico"), 204)
	css := get(a, "GET", "/style.css")
	mustStatus(t, css, 200)
	if !strings.Contains(body(css), ".chroma") || !strings.Contains(body(css), "prefers-color-scheme: dark") {
		t.Fatal("stylesheet should include light+dark code highlighting")
	}
	if ct := css.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("css content-type %q", ct)
	}
}

func TestStylesheetPrintAndColourSchemeScoping(t *testing.T) {
	a, _, _ := newTest(t, nil)
	css := body(get(a, "GET", "/style.css"))
	mustContain(t, css, "@media print", "@media (min-width: 72rem)", ".anchor::before", ".toc-side")
	// Dark palettes (page and code) apply to screens only, so print is always light.
	mustContain(t, css, "@media screen and (prefers-color-scheme: dark)")
	mustNotContain(t, css, "@media (prefers-color-scheme: dark)")
	// Light and dark code themes are mutually exclusive (see highlightCSS): the
	// light one must not apply unconditionally, or its token colours leak into dark.
	mustContain(t, css, "@media print, (prefers-color-scheme: light), (prefers-color-scheme: no-preference)")
	if i, m := strings.Index(css, ".chroma .k"), strings.Index(css, "@media print, (prefers-color-scheme: light)"); i < m {
		t.Fatalf("light code theme (.chroma .k at %d) appears before its media query (%d)", i, m)
	}
	mustContain(t, css, "@page")
	// Nested lists must not add a paragraph-sized gap after each sub-list.
	mustContain(t, css, "li > ul, li > ol { margin: 0; }")
}

func TestEmbedSwappedForSymlinkAfterIndexingIsRefused(t *testing.T) {
	outside := t.TempDir()
	writeVault(t, outside, "evil.png", "outside-secret")
	a, _, root := newTest(t, map[string]string{
		"n.md":    pubNoteSrc("page12345", "![[pic.png]]"),
		"pic.png": "inside-image",
	})
	sum := sha256.Sum256([]byte("inside-image"))
	url := "/" + hex.EncodeToString(sum[:])[:12] + ".png"
	mustStatus(t, get(a, "GET", url), 200)

	// Between scans, someone replaces the file with a symlink out of the vault.
	os.Remove(root + "/pic.png")
	if err := os.Symlink(outside+"/evil.png", root+"/pic.png"); err != nil {
		t.Fatal(err)
	}
	rec := get(a, "GET", url)
	mustStatus(t, rec, 404)
	if strings.Contains(body(rec), "outside-secret") {
		t.Fatal("followed a symlink out of the vault")
	}
}

func TestRangeRequestsOnEmbeds(t *testing.T) {
	a, _, _ := newTest(t, map[string]string{
		"n.md":     pubNoteSrc("page12345", "![[clip.mp4]]"),
		"clip.mp4": "0123456789",
	})
	sum := sha256.Sum256([]byte("0123456789"))
	url := "/" + hex.EncodeToString(sum[:])[:12] + ".mp4"
	req := httptest.NewRequest("GET", url, nil)
	req.Header.Set("Range", "bytes=2-4")
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	mustStatus(t, rec, 206)
	if body(rec) != "234" {
		t.Fatalf("range body %q", body(rec))
	}
}

func TestConcurrentServeWhileRescanning(t *testing.T) {
	a, sc, root := newTest(t, map[string]string{"n.md": pubNoteSrc("page12345", "v0")})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					get(a, "GET", "/page12345")
					get(a, "GET", "/nope")
				}
			}
		}()
	}
	for i := 0; i < 30; i++ {
		slug := "page12345"
		if i%2 == 1 {
			slug = "other12345"
		}
		writeVault(t, root, "n.md", pubNoteSrc(slug, strings.Repeat("x", i+1)))
		if _, err := sc.scan(); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
}
