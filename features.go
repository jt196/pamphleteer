package main

import (
	"bytes"
	"fmt"
	"html/template"
	"regexp"
	"strings"
	"time"
	// The scratch image has no zoneinfo; embedding it lets the TZ variable work.
	_ "time/tzdata"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/util"
)

// ---- raw HTML ----

// htmlPolicy is the only way raw HTML from a note reaches a page. It is a strict
// allowlist: no scripts, styles, classes, ids, event handlers or relative URLs.
var htmlPolicy = newHTMLPolicy()

func newHTMLPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements(
		"details", "summary", "div", "span", "p", "br", "hr", "kbd", "sub", "sup",
		"mark", "u", "s", "del", "ins", "small", "abbr", "b", "i", "em", "strong",
		"code", "pre", "blockquote", "ul", "ol", "li", "dl", "dt", "dd",
		"h2", "h3", "h4", "h5", "h6",
		"table", "thead", "tbody", "tfoot", "tr", "th", "td", "caption",
		"figure", "figcaption",
	)
	p.AllowAttrs("open").OnElements("details")
	p.AllowAttrs("title", "lang", "dir").Globally()
	p.AllowAttrs("colspan", "rowspan").Matching(bluemonday.Integer).OnElements("td", "th")
	p.AllowAttrs("href").OnElements("a")
	p.AllowAttrs("src", "alt").OnElements("img")
	p.AllowAttrs("width", "height").Matching(bluemonday.Number).OnElements("img")
	p.AllowURLSchemes("http", "https", "mailto")
	p.AllowRelativeURLs(false) // a relative path would name a vault file
	p.RequireNoReferrerOnLinks(true)
	return p
}

var imgSrcRe = regexp.MustCompile(`(?is)<img\b[^>]*?\bsrc\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)

// writeSanitized emits raw HTML through the policy. Local-file <img> tags cannot
// survive it (they would expose a vault path), so they are counted and reported.
func (r *resolver) writeSanitized(w util.BufWriter, raw string) {
	for _, m := range imgSrcRe.FindAllStringSubmatch(raw, -1) {
		if !isExternal(strings.TrimSpace(m[1] + m[2] + m[3])) {
			r.omitted++
		}
	}
	_, _ = w.WriteString(htmlPolicy.Sanitize(raw))
}

func (x *nodeRenderers) renderRawHTML(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	raw := n.(*ast.RawHTML)
	var b bytes.Buffer
	for i := 0; i < raw.Segments.Len(); i++ {
		seg := raw.Segments.At(i)
		b.Write(seg.Value(source))
	}
	x.r.writeSanitized(w, b.String())
	return ast.WalkSkipChildren, nil
}

func (x *nodeRenderers) renderHTMLBlock(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	hb := n.(*ast.HTMLBlock)
	var b bytes.Buffer
	for i := 0; i < hb.Lines().Len(); i++ {
		seg := hb.Lines().At(i)
		b.Write(seg.Value(source))
	}
	if hb.HasClosure() {
		b.Write(hb.ClosureLine.Value(source))
	}
	x.r.writeSanitized(w, b.String())
	_ = w.WriteByte('\n')
	return ast.WalkSkipChildren, nil
}

// ---- heading permalinks ----

func headingID(h *ast.Heading) string {
	v, ok := h.AttributeString("id")
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case []byte:
		return string(t)
	case string:
		return t
	}
	return ""
}

// renderHeading adds a permalink. The "#" is drawn by CSS, so it is not part of
// the page text (copy/paste, meta description) and needs no script to reveal.
func (x *nodeRenderers) renderHeading(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	h := n.(*ast.Heading)
	id := headingID(h)
	if entering {
		if id != "" {
			fmt.Fprintf(w, `<h%d id="%s">`, h.Level, esc(id))
		} else {
			fmt.Fprintf(w, `<h%d>`, h.Level)
		}
		return ast.WalkContinue, nil
	}
	if id != "" {
		fmt.Fprintf(w, `<a class="anchor" href="#%s" aria-label="Link to this section"></a>`, esc(id))
	}
	fmt.Fprintf(w, "</h%d>\n", h.Level)
	return ast.WalkContinue, nil
}

// ---- table of contents ----

type tocEntry struct {
	level int
	id    string
	text  string
}

func collectTOC(doc ast.Node, src []byte) []tocEntry {
	var out []tocEntry
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		h, ok := n.(*ast.Heading)
		if !ok || !entering {
			return ast.WalkContinue, nil
		}
		if id, text := headingID(h), strings.TrimSpace(nodeText(h, src)); h.Level <= 4 && id != "" && text != "" {
			out = append(out, tocEntry{level: h.Level, id: id, text: text})
		}
		return ast.WalkSkipChildren, nil
	})
	return out
}

func tocList(es []tocEntry) string {
	base := es[0].level
	for _, e := range es {
		if e.level < base {
			base = e.level
		}
	}
	var b strings.Builder
	b.WriteString("<ul>")
	prev := base
	for i, e := range es {
		lvl := e.level
		if i > 0 {
			if lvl > prev+1 {
				lvl = prev + 1 // never skip a nesting level
			}
			switch {
			case lvl > prev:
				b.WriteString("<ul>")
			case lvl < prev:
				for j := prev; j > lvl; j-- {
					b.WriteString("</li></ul>")
				}
				b.WriteString("</li>")
			default:
				b.WriteString("</li>")
			}
		}
		prev = lvl
		b.WriteString(`<li><a href="#` + esc(e.id) + `">` + esc(e.text) + `</a>`)
	}
	for j := prev; j > base; j-- {
		b.WriteString("</li></ul>")
	}
	b.WriteString("</li></ul>")
	return b.String()
}

// tocHTML returns two renderings of the same list: a collapsed <details> for
// narrow screens and a plain panel for wide ones. CSS media queries pick one, so
// it needs neither script nor newer CSS features. Notes with fewer than three
// headings get no contents list.
func tocHTML(es []tocEntry) (mobile, side template.HTML) {
	if len(es) < 3 {
		return "", ""
	}
	list := tocList(es)
	mobile = template.HTML(`<details class="toc toc-mobile"><summary>Contents</summary><nav aria-label="Contents">` + list + `</nav></details>`)
	side = template.HTML(`<nav class="toc toc-side" aria-label="Contents"><p class="toc-title">Contents</p>` + list + `</nav>`)
	return mobile, side
}

// ---- dates ----

const dateLayout = "2 Jan 2006"

var frontmatterDateLayouts = []string{
	"2006-01-02", "2006-01-02T15:04:05", "2006-01-02 15:04:05",
	"2006-01-02T15:04", "2006-01-02 15:04",
}

func parseFrontmatterDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.In(time.Local), true
	}
	for _, l := range frontmatterDateLayouts {
		if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// metaLine is "Created <date> · Updated <date>". Updated is the file's mtime
// (Obsidian writes no modified date; Syncthing preserves mtimes). Created can
// only come from frontmatter: a file's own creation time is just when it landed
// on this machine.
func (r *resolver) metaLine(p *pubNote) string {
	if !r.showDates {
		return ""
	}
	updated := time.Unix(0, p.key.mtime).In(time.Local)
	created, ok := parseFrontmatterDate(p.created)
	if !ok {
		return "Updated " + updated.Format(dateLayout)
	}
	if created.Format(dateLayout) == updated.Format(dateLayout) {
		return "Created " + created.Format(dateLayout)
	}
	return "Created " + created.Format(dateLayout) + " · Updated " + updated.Format(dateLayout)
}
