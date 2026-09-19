package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMeta(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantPublish bool
		wantSlug    string
		wantTitle   string
	}{
		{"published", "---\npublish: true\nslug: abc12345\n---\nbody", true, "abc12345", "stem"},
		{"title override", "---\npublish: true\nslug: a\ntitle: My Title\n---\n", true, "a", "My Title"},
		{"quoted true is not a boolean", "---\npublish: \"true\"\nslug: a\n---\n", false, "a", "stem"},
		{"yes is not true", "---\npublish: yes\nslug: a\n---\n", false, "a", "stem"},
		{"false", "---\npublish: false\nslug: a\n---\n", false, "a", "stem"},
		{"absent", "---\nslug: a\n---\n", false, "a", "stem"},
		{"no frontmatter", "just text\npublish: true\n", false, "", "stem"},
		{"malformed yaml", "---\npublish: [\nslug: a\n---\n", false, "", "stem"},
		{"unterminated", "---\npublish: true\nslug: a\n", false, "", "stem"},
		{"crlf", "---\r\npublish: true\r\nslug: a\r\n---\r\nbody", true, "a", "stem"},
		{"bom", "\xef\xbb\xbf---\npublish: true\nslug: a\n---\n", true, "a", "stem"},
		{"numeric slug keeps its text", "---\npublish: true\nslug: 1234567890\n---\n", true, "1234567890", "stem"},
		{"publish in body only", "---\nslug: a\n---\npublish: true\n", false, "a", "stem"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pub, slug, title, _ := parseMeta([]byte(c.in), "stem")
			if pub != c.wantPublish || slug != c.wantSlug || title != c.wantTitle {
				t.Fatalf("got (%v, %q, %q), want (%v, %q, %q)", pub, slug, title, c.wantPublish, c.wantSlug, c.wantTitle)
			}
		})
	}
}

func TestParseMetaCreated(t *testing.T) {
	for in, want := range map[string]string{
		"---\ncreated: 2026-09-17\n---\n":           "2026-09-17",
		"---\ncreated: 2026-09-17T10:30:00Z\n---\n": "2026-09-17T10:30:00Z",
		"---\ncreated: \"2026-09-17 10:30\"\n---\n": "2026-09-17 10:30",
		"---\ncreated: [1, 2]\n---\n":               "",
		"---\ntitle: x\n---\n":                      "",
	} {
		if _, _, _, got := parseMeta([]byte(in), "s"); got != want {
			t.Errorf("created for %q = %q, want %q", in, got, want)
		}
	}
}

func TestOnlyPublishedNotesWithValidUniqueSlugsAreServed(t *testing.T) {
	a, _, _ := newTest(t, map[string]string{
		"good.md":       pubNoteSrc("good12345", "hello"),
		"draft.md":      "---\npublish: false\nslug: draft12345\n---\nprivate",
		"plain.md":      "no frontmatter at all",
		"noslug.md":     "---\npublish: true\n---\nno slug must not be guessable by filename",
		"badslug.md":    pubNoteSrc(`"a/b"`, "x"),
		"dotslug.md":    pubNoteSrc("a.b", "x"),
		"reserved.md":   pubNoteSrc("healthz", "x"),
		"dupe/one.md":   pubNoteSrc("dupe12345", "one"),
		"dupe/two.md":   pubNoteSrc("dupe12345", "two"),
		"sub/deep.md":   pubNoteSrc("deep12345", "deep"),
		"Upper Case.md": pubNoteSrc("Mixed_Case-1", "x"),
	})
	got := servedSlugs(a)
	want := map[string]bool{"good12345": true, "deep12345": true, "Mixed_Case-1": true}
	if len(got) != len(want) {
		t.Fatalf("served %v, want %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("served %v, want %v", got, want)
		}
	}
	// Neither file of a duplicated slug may be served, and no fallback slug
	// (filename) may exist for a note without one.
	for _, p := range []string{"/dupe12345", "/noslug", "/draft", "/draft12345", "/healthz-note"} {
		if rec := get(a, "GET", p); rec.Code != 404 && p != "/healthz-note" {
			t.Fatalf("%s: status %d, want 404", p, rec.Code)
		}
	}
}

func TestStaleAndForeignCopiesAreNeverPublished(t *testing.T) {
	outside := t.TempDir()
	writeVault(t, outside, "secret.md", pubNoteSrc("outside12345", "outside the vault"))
	writeVault(t, outside, "dir/inner.md", pubNoteSrc("outdir12345", "outside dir"))

	a, _, root := newTest(t, map[string]string{
		"good.md":                                       pubNoteSrc("good12345", "hello"),
		".stversions/good~20260101-000000.md":           pubNoteSrc("stale12345", "old version"),
		".obsidian/plugins/x/note.md":                   pubNoteSrc("obsid12345", "config dir"),
		"proj/.git/notes.md":                            pubNoteSrc("git123456", "git dir"),
		"proj/node_modules/pkg/README.md":               pubNoteSrc("nodem12345", "node_modules"),
		"@eaDir/thumb.md":                               pubNoteSrc("eadir12345", "synology"),
		"#recycle/gone.md":                              pubNoteSrc("recyc12345", "recycle bin"),
		"good.sync-conflict-20260101-000000-ABCDEFG.md": pubNoteSrc("confl12345", "conflict copy"),
		".hidden.md":                                    pubNoteSrc("hidfile1234", "hidden file"),
	})
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "dir"), filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	// Symlink to an in-vault published note: following it would duplicate the slug.
	if err := os.Symlink(filepath.Join(root, "good.md"), filepath.Join(root, "alias.md")); err != nil {
		t.Fatal(err)
	}
	sc := newScanner(root, testLogger(), &a.snap)
	if _, err := sc.scan(); err != nil {
		t.Fatal(err)
	}

	got := servedSlugs(a)
	if len(got) != 1 || !got["good12345"] {
		t.Fatalf("served %v, want only good12345", got)
	}
}

func TestUnpublishAndRotate(t *testing.T) {
	a, sc, root := newTest(t, map[string]string{"n.md": pubNoteSrc("first12345", "v1")})
	mustStatus(t, get(a, "GET", "/first12345"), 200)

	// Rotate: same file, new slug of the same length (the plugin's real edit).
	writeVault(t, root, "n.md", pubNoteSrc("secnd12345", "v1"))
	if changed, err := sc.scan(); err != nil || !changed {
		t.Fatalf("scan after rotate: changed=%v err=%v", changed, err)
	}
	mustStatus(t, get(a, "GET", "/first12345"), 404)
	mustStatus(t, get(a, "GET", "/secnd12345"), 200)

	// Edit propagates.
	writeVault(t, root, "n.md", pubNoteSrc("secnd12345", "edited body text"))
	sc.scan()
	if !strings.Contains(body(get(a, "GET", "/secnd12345")), "edited body text") {
		t.Fatal("edit did not propagate")
	}

	// Unpublish.
	writeVault(t, root, "n.md", "---\npublish: false\nslug: secnd12345\n---\nv1")
	sc.scan()
	mustStatus(t, get(a, "GET", "/secnd12345"), 404)

	// Delete a published note.
	writeVault(t, root, "n.md", pubNoteSrc("secnd12345", "back"))
	sc.scan()
	mustStatus(t, get(a, "GET", "/secnd12345"), 200)
	os.Remove(filepath.Join(root, "n.md"))
	sc.scan()
	mustStatus(t, get(a, "GET", "/secnd12345"), 404)
}

func TestScanIsNoOpWhenNothingChanged(t *testing.T) {
	_, sc, _ := newTest(t, map[string]string{"n.md": pubNoteSrc("first12345", "v1")})
	if changed, err := sc.scan(); err != nil || changed {
		t.Fatalf("second scan: changed=%v err=%v, want unchanged", changed, err)
	}
}

func TestMissingVaultKeepsPreviousSnapshot(t *testing.T) {
	a, sc, root := newTest(t, map[string]string{"n.md": pubNoteSrc("first12345", "v1")})
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.scan(); err == nil {
		t.Fatal("scan of a vanished vault should error")
	}
	mustStatus(t, get(a, "GET", "/first12345"), 200)
}

// makeBenchVault builds a vault shaped like a real one: thousands of notes and
// attachments across many dirs, plus a hidden dev dir the scanner must skip.
func makeBenchVault(tb testing.TB) string {
	tb.Helper()
	root := tb.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			tb.Fatal(err)
		}
	}
	for i := 0; i < 2800; i++ {
		write(fmt.Sprintf("area%d/topic%d/Note %d.md", i%40, i%300, i), "---\ntags: [a]\n---\n# Note\n\n"+strings.Repeat("text ", 200))
	}
	for i := 0; i < 6000; i++ {
		write(fmt.Sprintf("area%d/topic%d/img/pic%d.png", i%40, i%300, i), "x")
	}
	for i := 0; i < 2500; i++ {
		write(fmt.Sprintf("proj/.venv/lib/mod%d.md", i), pubNoteSrc(fmt.Sprintf("leak%05d", i), "x"))
	}
	write("area0/topic0/Published.md", pubNoteSrc("BenchPub01", "hello [[Note 5]]"))
	return root
}

// BenchmarkScanUnchanged is the steady-state polling cost: a full vault walk
// where nothing has changed.
func BenchmarkScanUnchanged(b *testing.B) {
	root := makeBenchVault(b)
	a := newApp()
	sc := newScanner(root, testLogger(), &a.snap)
	if _, err := sc.scan(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		if changed, err := sc.scan(); err != nil || changed {
			b.Fatalf("changed=%v err=%v", changed, err)
		}
	}
}
