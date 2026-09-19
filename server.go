package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

const csp = "default-src 'none'; img-src 'self' https:; media-src 'self' https:; " +
	"style-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// app serves straight from the current snapshot. Request paths are only ever
// used as map keys: no request can reach the filesystem except through a path
// the scanner already vetted and recorded in the snapshot.
type app struct {
	snap    atomic.Pointer[snapshot]
	css     []byte
	cssETag string
}

func newApp() *app {
	css, err := assets.ReadFile("assets/style.css")
	if err != nil {
		panic(err)
	}
	css = append(css, []byte("\n"+highlightCSS())...)
	sum := sha256.Sum256(css)
	return &app{css: css, cssETag: `"` + hex.EncodeToString(sum[:8]) + `"`}
}

func baseHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	h.Set("Content-Security-Policy", csp)
}

func (a *app) notFound(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("<!doctype html><meta charset=utf-8><title>Not found</title><p>Not found\n"))
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	baseHeaders(w.Header())

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	switch r.URL.Path {
	case "/healthz":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("ok\n"))
		return
	case "/robots.txt":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
		return
	case "/favicon.ico":
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusNoContent)
		return
	case "/style.css":
		h := w.Header()
		h.Set("Content-Type", "text/css; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", a.cssETag)
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(a.css))
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/")
	snap := a.snap.Load()
	if snap == nil {
		a.notFound(w)
		return
	}

	if pg, ok := snap.pages[name]; ok {
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", pg.etag)
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(pg.body))
		return
	}

	if em, ok := snap.embeds[name]; ok {
		a.serveEmbed(w, r, em)
		return
	}

	a.notFound(w)
}

func (a *app) serveEmbed(w http.ResponseWriter, r *http.Request, em embedInfo) {
	fi, err := os.Lstat(em.path)
	if err != nil || !fi.Mode().IsRegular() {
		a.notFound(w)
		return
	}
	f, err := os.Open(em.path)
	if err != nil {
		a.notFound(w)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		a.notFound(w)
		return
	}
	h := w.Header()
	h.Set("Content-Type", em.ctype)
	h.Set("Cache-Control", "private, max-age=3600")
	if em.ctype == "application/pdf" {
		// The page CSP would stop browsers' built-in PDF viewers.
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
	}
	http.ServeContent(w, r, "", st.ModTime(), f)
}
