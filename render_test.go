package main

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"regexp"
	"strings"
	"testing"
)

const mainSlug = "main12345"

// renderMain publishes one note ("Main.md") alongside the given extra vault
// files and returns its rendered page.
func renderMain(t *testing.T, mainBody string, extra map[string]string) string {
	t.Helper()
	files := map[string]string{"Main.md": pubNoteSrc(mainSlug, mainBody)}
	for k, v := range extra {
		files[k] = v
	}
	a, _, _ := newTest(t, files)
	rec := get(a, "GET", "/"+mainSlug)
	mustStatus(t, rec, 200)
	return body(rec)
}

func mustContain(t *testing.T, html string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(html, s) {
			t.Errorf("output should contain %q\n--- output ---\n%s", s, html)
		}
	}
}

func mustNotContain(t *testing.T, html string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(html, s) {
			t.Errorf("output must not contain %q\n--- output ---\n%s", s, html)
		}
	}
}

func TestWikilinks(t *testing.T) {
	html := renderMain(t,
		"See [[Other]], [[Other|the other one]], [[Other#Some Heading]], [[folder/Other]], "+
			"[[Secret]] and [[Secret|alias text]] and [[Missing Note]].",
		map[string]string{
			"Other.md":  pubNoteSrc("other12345", "x"),
			"Secret.md": "---\npublish: false\n---\nprivate",
		})
	mustContain(t, html,
		`<a href="/other12345">Other</a>`,
		`<a href="/other12345">the other one</a>`,
		// Unpublished / missing targets keep their text but never become links.
		"Secret", "alias text", "Missing Note",
	)
	mustNotContain(t, html, "[[", "]]", "/secret", "Secret.md", "href=\"/Secret")
	if n := strings.Count(html, `href="/`); n < 3 {
		t.Errorf("expected several links to the published note, got %d", n)
	}
}

func TestWikilinkSyntaxInsideCodeIsLeftAlone(t *testing.T) {
	html := renderMain(t,
		"Use `[[ -f x ]]` in bash.\n\n```bash\nif [[ -d /tmp ]]; then echo hi; fi\n```\n",
		nil)
	mustContain(t, html, "[[ -f x ]]", "[[ -d /tmp ]]")
}

func TestObsidianCommentsAreStripped(t *testing.T) {
	html := renderMain(t, strings.Join([]string{
		"visible one %%hidden inline%% visible two",
		"",
		"%%",
		"hidden multi-line",
		"still hidden",
		"%%",
		"",
		"after block",
		"",
		"inline code `%%` stays and %% hidden again %% done",
		"",
		"```c",
		"printf(\"%%d %%s\");",
		"```",
		"",
		"final paragraph %% unterminated comment tail-secret",
		"next line tail-secret-2",
	}, "\n"), nil)
	mustContain(t, html, "visible one", "visible two", "after block", "stays", "done", "final paragraph")
	mustContain(t, html, "printf", "%%d %%s")
	mustNotContain(t, html, "hidden inline", "hidden multi-line", "still hidden", "hidden again", "tail-secret", "unterminated")
}

func TestRawHTMLIsNotPassedThrough(t *testing.T) {
	html := renderMain(t, strings.Join([]string{
		"para with <script>alert(1)</script> inline and <img src=x onerror=alert(2)> and line<br>break",
		"",
		"<!-- private html comment -->",
		"",
		"<script>alert(3)</script>",
		"",
		"<div onclick=\"alert(4)\">block</div>",
		"",
		"<a href=\"javascript:alert(5)\">js link</a> [md js](javascript:alert(6))",
	}, "\n"), nil)
	// Tags and attributes are dropped. Text that sat between dropped tags
	// (e.g. the "alert(1)" inside <script>) is left as inert escaped text.
	mustNotContain(t, html, "<script", "onerror", "onclick", "private html comment", "javascript:", "<img", "<div", "<a ")
	mustContain(t, html, "<br>", "js link")
	// Block-level HTML is dropped whole, text and all (reported in the log).
	mustNotContain(t, html, "block</", "omitted")
}

func TestFrontmatterIsNeverRendered(t *testing.T) {
	src := "---\npublish: true\nslug: " + mainSlug + "\ntitle: My Title\nsecret_field: hunter2\ntags: [a, b]\n---\nBody text\n"
	a, _, _ := newTest(t, map[string]string{"Main.md": src})
	html := body(get(a, "GET", "/"+mainSlug))
	mustContain(t, html, "<title>My Title</title>", "<h1>My Title</h1>", "Body text")
	mustNotContain(t, html, "hunter2", "secret_field", mainSlug, "publish")
}

func TestTitleIsEscaped(t *testing.T) {
	src := "---\npublish: true\nslug: " + mainSlug + "\ntitle: \"<script>alert(1)</script> & co\"\n---\nx"
	a, _, _ := newTest(t, map[string]string{"Main.md": src})
	html := body(get(a, "GET", "/"+mainSlug))
	mustNotContain(t, html, "<script>")
	mustContain(t, html, "&lt;script&gt;")
}

func TestDuplicateLeadingH1IsDropped(t *testing.T) {
	a, _, _ := newTest(t, map[string]string{
		"Note Title.md": pubNoteSrc(mainSlug, "# Note Title\n\nbody\n"),
	})
	html := body(get(a, "GET", "/"+mainSlug))
	if n := strings.Count(html, "<h1"); n != 1 {
		t.Fatalf("want exactly one <h1>, got %d:\n%s", n, html)
	}
	mustContain(t, html, "body")

	// A different heading is real content and stays.
	a, _, _ = newTest(t, map[string]string{
		"Note Title.md": pubNoteSrc(mainSlug, "# Something else\n\nbody\n"),
	})
	html = body(get(a, "GET", "/"+mainSlug))
	if n := strings.Count(html, "<h1"); n != 2 {
		t.Fatalf("want two <h1>, got %d:\n%s", n, html)
	}
}

var hashedImg = regexp.MustCompile(`<img src="/([0-9a-f]{12})\.png"`)

func TestEmbedsAreServedByContentHashNeverByName(t *testing.T) {
	png := "\x89PNG\r\n\x1a\nfake-png-bytes"
	sum := sha256.Sum256([]byte(png))
	wantHash := hex.EncodeToString(sum[:])[:12]

	a, _, _ := newTest(t, map[string]string{
		"Main.md": pubNoteSrc(mainSlug, strings.Join([]string{
			"wiki: ![[photo.png]]",
			"sized: ![[photo.png|300]]",
			"alt: ![[photo.png|a caption]]",
			"md: ![my alt](Attachments/photo.png)",
			"encoded: ![enc](Attachments/photo%20two.png)",
			"missing: ![[missing.png]] ![gone](nope/gone.png)",
			"escape: ![x](../secret/hidden.png)",
		}, "\n\n")),
		"Attachments/photo.png":     png,
		"Attachments/photo two.png": "second-image",
		"secret/hidden.png":         "not referenced by any published note",
	})
	html := body(get(a, "GET", "/"+mainSlug))

	if m := hashedImg.FindStringSubmatch(html); m == nil || m[1] != wantHash {
		t.Fatalf("expected hashed image %s, got:\n%s", wantHash, html)
	}
	mustContain(t, html, `width="300"`, `alt="a caption"`, `alt="my alt"`)
	// No vault filename or path may appear anywhere in the page.
	mustNotContain(t, html, "photo.png", "photo two", "Attachments", "hidden.png", "missing.png", "gone.png", "secret/")

	// The hashed URL serves the exact bytes with a fixed content type.
	rec := get(a, "GET", "/"+wantHash+".png")
	mustStatus(t, rec, 200)
	if body(rec) != png {
		t.Fatalf("embed bytes differ")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type = %q", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("embeds need nosniff")
	}

	// An attachment no published note references is unreachable, by hash or name.
	un := sha256.Sum256([]byte("not referenced by any published note"))
	mustStatus(t, get(a, "GET", "/"+hex.EncodeToString(un[:])[:12]+".png"), 404)
	for _, p := range []string{"/hidden.png", "/secret/hidden.png", "/Attachments/photo.png", "/photo.png"} {
		mustStatus(t, get(a, "GET", p), 404)
	}
}

func TestSymlinkedAttachmentIsNotEmbedded(t *testing.T) {
	outside := t.TempDir()
	writeVault(t, outside, "leak.png", "outside-the-vault")

	root := t.TempDir()
	writeVault(t, root, "Main.md", pubNoteSrc(mainSlug, "![[leak.png]]"))
	if err := osSymlink(outside+"/leak.png", root+"/leak.png"); err != nil {
		t.Fatal(err)
	}
	a := newApp()
	sc := newScanner(root, testLogger(), &a.snap)
	if _, err := sc.scan(); err != nil {
		t.Fatal(err)
	}
	html := body(get(a, "GET", "/"+mainSlug))
	mustNotContain(t, html, "<img", "leak.png")
}

func TestExternalLinksAndMedia(t *testing.T) {
	html := renderMain(t, strings.Join([]string{
		"![ext](https://example.com/a.png)",
		"[clip](https://example.com/clip.mp4)",
		"[song](https://example.com/song.mp3)",
		"[site](https://example.com/page?x=1&y=2)",
		"[mail](mailto:a@example.com)",
		"[rel](some/local.md)",
		"[js](javascript:alert(1))",
		"[data](data:text/html,<b>x</b>)",
	}, "\n\n"), nil)
	mustContain(t, html,
		`<img src="https://example.com/a.png" alt="ext" loading="lazy" referrerpolicy="no-referrer">`,
		`<video src="https://example.com/clip.mp4" controls preload="none">`,
		`<audio src="https://example.com/song.mp3" controls preload="none">`,
		`<a href="https://example.com/page?x=1&amp;y=2" rel="noopener noreferrer">site</a>`,
		`<a href="mailto:a@example.com">mail</a>`,
		"rel", // relative link text survives ...
	)
	mustNotContain(t, html, `href="some/local.md"`, "javascript:", "data:text", `href="some`)
}

func TestGFMAndHighlighting(t *testing.T) {
	html := renderMain(t, strings.Join([]string{
		"| a | b |", "|---|---|", "| 1 | 2 |",
		"",
		"~~gone~~ and a footnote[^1]",
		"",
		"- [x] done task",
		"",
		"```go",
		"func main() { fmt.Println(\"hi\") }",
		"```",
		"",
		"```unknownlang",
		"<not html> & stuff",
		"```",
		"",
		"[^1]: The note.",
	}, "\n"), nil)
	mustContain(t, html, "<table>", "<del>gone</del>", `type="checkbox"`, `class="chroma"`, "footnote", "&lt;not html&gt;")
	mustNotContain(t, html, "style=")
}

func TestPageHasNoScriptsAndIsNoIndex(t *testing.T) {
	html := renderMain(t, "plain text body that is used as the description", nil)
	mustNotContain(t, html, "<script")
	mustContain(t, html,
		`<meta name="robots" content="noindex, nofollow, noarchive">`,
		`<meta property="og:title"`,
		`<meta property="og:description" content="plain text body`,
		`<link rel="stylesheet" href="/style.css">`)
}

func TestStripCommentsUnit(t *testing.T) {
	cases := map[string]string{
		"a %%b%% c":            "a  c",
		"a %%b%% c %%d%% e":    "a  c  e",
		"no comments":          "no comments",
		"`%%` keep %%drop%%":   "`%%` keep ",
		"start %% open":        "start ",
		"```\n%% keep %%\n```": "```\n%% keep %%\n```",
		// Delimiter lines leave a blank line each, which Markdown collapses.
		"x\n%%\nhidden\n%%\ny": "x\n\n\ny",
		"x\n%%\nnever closed":  "x\n\n",
	}
	for in, want := range cases {
		if got := stripComments(in); got != want {
			t.Errorf("stripComments(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOmittedRawHTMLIsReportedInTheLog(t *testing.T) {
	root := t.TempDir()
	writeVault(t, root, "Main.md", pubNoteSrc(mainSlug, "<details>\nhidden content\n</details>\n\nand <span>inline</span> tag\n"))
	writeVault(t, root, "Clean.md", pubNoteSrc("clean12345", "nothing to see, with a line<br>break"))

	var logs strings.Builder
	a := newApp()
	sc := newScanner(root, slog.New(slog.NewTextHandler(&logs, nil)), &a.snap)
	if _, err := sc.scan(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "raw HTML omitted from published note") || !strings.Contains(logs.String(), "Main.md") {
		t.Fatalf("expected a warning naming Main.md, got:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "Clean.md") {
		t.Fatalf("an allowed <br> must not be reported:\n%s", logs.String())
	}
}
