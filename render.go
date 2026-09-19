package main

import (
	"bytes"
	"embed"
	"fmt"
	"html"
	"html/template"
	"net/url"
	"path"
	"regexp"
	"strings"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

//go:embed assets/*
var assets embed.FS

var pageTmpl = template.Must(template.ParseFS(assets, "assets/page.html"))

type pageData struct {
	Title       string
	Description string
	Body        template.HTML
}

// resolver maps the names used inside notes ([[Note]], ![[img.png]]) onto
// published notes and embeddable attachments. Nothing outside those two sets
// can ever be linked, so an unpublished target degrades to plain text.
type resolver struct {
	notes     []*pubNote
	notesLC   []string
	attByBase map[string][]vaultFile
	hash      func(vaultFile) (string, error)
	embeds    map[string]embedInfo
	omitted   int // raw HTML fragments dropped from the note being rendered
}

func newResolver(pubs []*pubNote, atts []vaultFile, hash func(vaultFile) (string, error)) *resolver {
	r := &resolver{
		notes:     pubs,
		attByBase: map[string][]vaultFile{},
		hash:      hash,
		embeds:    map[string]embedInfo{},
	}
	for _, p := range pubs {
		r.notesLC = append(r.notesLC, strings.ToLower(strings.TrimSuffix(p.rel, path.Ext(p.rel))))
	}
	for _, a := range atts {
		base := strings.ToLower(path.Base(a.rel))
		r.attByBase[base] = append(r.attByBase[base], a)
	}
	return r
}

func normName(name string) string {
	n := strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	n = strings.TrimPrefix(n, "./")
	n = strings.TrimPrefix(n, "/")
	return strings.ToLower(n)
}

// findNote resolves a wikilink target among published notes only. Ambiguity
// resolves to the shortest path, like Obsidian.
func (r *resolver) findNote(name string) *pubNote {
	n := normName(name)
	n = strings.TrimSuffix(n, ".md")
	if n == "" {
		return nil
	}
	var best *pubNote
	bestLen := 0
	for i, cand := range r.notesLC {
		if cand == n || strings.HasSuffix(cand, "/"+n) {
			if best == nil || len(cand) < bestLen {
				best, bestLen = r.notes[i], len(cand)
			}
		}
	}
	return best
}

func (r *resolver) findAttachment(name string) (vaultFile, bool) {
	n := normName(name)
	if n == "" {
		return vaultFile{}, false
	}
	var best vaultFile
	found := false
	for _, f := range r.attByBase[path.Base(n)] {
		rel := strings.ToLower(f.rel)
		if !strings.Contains(n, "/") || rel == n || strings.HasSuffix(rel, "/"+n) {
			if !found || len(f.rel) < len(best.rel) {
				best, found = f, true
			}
		}
	}
	return best, found
}

func (r *resolver) attachmentURL(f vaultFile) (string, bool) {
	h, err := r.hash(f)
	if err != nil {
		return "", false
	}
	ext := strings.ToLower(path.Ext(f.rel))
	name := h + ext
	r.embeds[name] = embedInfo{path: f.path, ctype: attachmentTypes[ext]}
	return "/" + name, true
}

// ---- wikilink syntax: [[target#heading|alias]] and ![[target|alias]] ----

var kindWiki = ast.NewNodeKind("WikiLink")

type wikiNode struct {
	ast.BaseInline
	embed   bool
	target  string
	heading string
	alias   string
}

func (n *wikiNode) Kind() ast.NodeKind { return kindWiki }
func (n *wikiNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"target": n.target}, nil)
}

func (n *wikiNode) display() string {
	switch {
	case n.alias != "":
		return n.alias
	case n.target != "":
		return n.target
	}
	return n.heading
}

// wikiParser is a real inline parser, so [[...]] inside code spans and fenced
// blocks (think bash's [[ -f x ]]) is left alone.
type wikiParser struct{}

func (wikiParser) Trigger() []byte { return []byte{'[', '!'} }

func (wikiParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, _ := block.PeekLine()
	i, embed := 0, false
	if len(line) > 0 && line[0] == '!' {
		i, embed = 1, true
	}
	if !bytes.HasPrefix(line[i:], []byte("[[")) {
		return nil
	}
	rest := line[i+2:]
	end := bytes.Index(rest, []byte("]]"))
	if end <= 0 {
		return nil
	}
	inner := string(rest[:end])
	if strings.ContainsAny(inner, "[]") {
		return nil
	}
	block.Advance(i + 2 + end + 2)
	target, alias, _ := strings.Cut(inner, "|")
	name, heading, _ := strings.Cut(target, "#")
	return &wikiNode{
		embed:   embed,
		target:  strings.TrimSpace(name),
		heading: strings.TrimSpace(heading),
		alias:   strings.TrimSpace(alias),
	}
}

// ---- rendering ----

var (
	imageExtRe = regexp.MustCompile(`(?i)\.(jpe?g|png|gif|webp|svg|avif)$`)
	videoExtRe = regexp.MustCompile(`(?i)\.(mp4|webm|mov|m4v|ogv)$`)
	audioExtRe = regexp.MustCompile(`(?i)\.(mp3|wav|ogg|m4a|opus|aac|flac)$`)
	sizeHintRe = regexp.MustCompile(`^\d+(x\d+)?$`)
	// Inline raw HTML is dropped except for a few attribute-free tags.
	allowedRawHTML = regexp.MustCompile(`(?i)^</?(br|details|summary|kbd|sub|sup|mark|u|s|del|ins|small|abbr|b|i|em|strong|code)\s*/?>$`)
)

func esc(s string) string { return html.EscapeString(s) }

func urlPath(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		return u[:i]
	}
	return u
}

func isExternal(u string) bool {
	l := strings.ToLower(u)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

func mediaTag(src, alt string) string {
	p := urlPath(src)
	switch {
	case imageExtRe.MatchString(p):
		return `<img src="` + esc(src) + `" alt="` + esc(alt) + `" loading="lazy" referrerpolicy="no-referrer">`
	case videoExtRe.MatchString(p):
		return `<video src="` + esc(src) + `" controls preload="none"></video>`
	case audioExtRe.MatchString(p):
		return `<audio src="` + esc(src) + `" controls preload="none"></audio>`
	}
	return ""
}

// writeAttachment emits an embedded local file. Files are only ever referred
// to by content hash, never by their vault name or path.
func (r *resolver) writeAttachment(w util.BufWriter, f vaultFile, alt, width, linkText string) {
	u, ok := r.attachmentURL(f)
	if !ok {
		return
	}
	switch ext := strings.ToLower(path.Ext(f.rel)); {
	case ext == ".pdf":
		fmt.Fprintf(w, `<a href="%s">%s</a>`, u, esc(linkText))
	case videoExtRe.MatchString(ext):
		fmt.Fprintf(w, `<video src="%s" controls preload="none"></video>`, u)
	case audioExtRe.MatchString(ext):
		fmt.Fprintf(w, `<audio src="%s" controls preload="none"></audio>`, u)
	default:
		wa := ""
		if width != "" {
			wa = ` width="` + esc(strings.SplitN(width, "x", 2)[0]) + `"`
		}
		fmt.Fprintf(w, `<img src="%s" alt="%s" loading="lazy"%s>`, u, esc(alt), wa)
	}
}

type nodeRenderers struct{ r *resolver }

func (x *nodeRenderers) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindWiki, x.renderWiki)
	reg.Register(ast.KindLink, x.renderLink)
	reg.Register(ast.KindImage, x.renderImage)
	reg.Register(ast.KindRawHTML, x.renderRawHTML)
	reg.Register(ast.KindHTMLBlock, x.renderHTMLBlock)
}

func (x *nodeRenderers) renderWiki(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	wn := n.(*wikiNode)
	display := wn.display()

	isAttachment := false
	if wn.embed {
		_, isAttachment = attachmentTypes[strings.ToLower(path.Ext(wn.target))]
	}
	if isAttachment {
		// Unresolvable attachments vanish rather than printing a vault filename.
		if f, ok := x.r.findAttachment(wn.target); ok {
			alt := ""
			width := ""
			if sizeHintRe.MatchString(wn.alias) {
				width = wn.alias
			} else {
				alt = wn.alias
			}
			linkText := wn.alias
			if linkText == "" || width != "" {
				linkText = path.Base(wn.target)
			}
			x.r.writeAttachment(w, f, alt, width, linkText)
		}
		return ast.WalkContinue, nil
	}

	// Links and note embeds: a published target becomes a link, anything else
	// (unpublished, missing) is plain text so no URL or existence leaks.
	if note := x.r.findNote(wn.target); note != nil {
		fmt.Fprintf(w, `<a href="/%s">%s</a>`, note.slug, esc(display))
	} else {
		w.WriteString(esc(display))
	}
	return ast.WalkContinue, nil
}

func linkKind(dest string) string {
	l := strings.ToLower(dest)
	switch {
	case strings.HasPrefix(l, "http://"), strings.HasPrefix(l, "https://"):
		return "external"
	case strings.HasPrefix(l, "mailto:"):
		return "mail"
	case strings.HasPrefix(l, "#"):
		return "anchor"
	}
	return "local"
}

func (x *nodeRenderers) renderLink(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	l := n.(*ast.Link)
	dest := string(l.Destination)
	switch linkKind(dest) {
	case "external":
		if m := mediaTag(dest, nodeText(n, source)); m != "" {
			if entering {
				w.WriteString(m)
			}
			return ast.WalkSkipChildren, nil
		}
		if entering {
			w.WriteString(`<a href="` + esc(dest) + `" rel="noopener noreferrer">`)
		} else {
			w.WriteString("</a>")
		}
	case "mail", "anchor":
		if entering {
			w.WriteString(`<a href="` + esc(dest) + `">`)
		} else {
			w.WriteString("</a>")
		}
	default:
		// Relative links point at vault paths; keep the text, drop the target.
	}
	return ast.WalkContinue, nil
}

func (x *nodeRenderers) renderImage(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	img := n.(*ast.Image)
	dest := string(img.Destination)
	alt := nodeText(n, source)

	if isExternal(dest) {
		if m := mediaTag(dest, alt); m != "" {
			w.WriteString(m)
		} else {
			w.WriteString(`<img src="` + esc(dest) + `" alt="` + esc(alt) + `" loading="lazy" referrerpolicy="no-referrer">`)
		}
		return ast.WalkSkipChildren, nil
	}

	name := urlPath(dest)
	if un, err := url.PathUnescape(name); err == nil {
		name = un
	}
	if f, ok := x.r.findAttachment(name); ok {
		x.r.writeAttachment(w, f, alt, "", alt)
	} else if alt != "" {
		w.WriteString(esc(alt))
	}
	return ast.WalkSkipChildren, nil
}

func (x *nodeRenderers) renderRawHTML(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	raw := n.(*ast.RawHTML)
	for i := 0; i < raw.Segments.Len(); i++ {
		seg := raw.Segments.At(i)
		tag := seg.Value(source)
		if allowedRawHTML.Match(tag) {
			w.Write(tag)
		} else {
			x.r.omitted++
		}
	}
	return ast.WalkSkipChildren, nil
}

// renderHTMLBlock drops block-level raw HTML entirely (including any text
// inside it) and counts it, so the loss is reported instead of silent.
func (x *nodeRenderers) renderHTMLBlock(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		x.r.omitted++
	}
	return ast.WalkSkipChildren, nil
}

func nodeText(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := c.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(src))
			if t.SoftLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(t.Value)
		case *wikiNode:
			b.WriteString(t.display())
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

func (r *resolver) markdown() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			extension.Footnote,
			highlighting.NewHighlighting(
				highlighting.WithStyle("github"),
				highlighting.WithGuessLanguage(false),
				highlighting.WithFormatOptions(chromahtml.WithClasses(true)),
			),
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
			parser.WithInlineParsers(util.Prioritized(wikiParser{}, 199)),
		),
		goldmark.WithRendererOptions(
			renderer.WithNodeRenderers(util.Prioritized(&nodeRenderers{r: r}, 100)),
		),
	)
}

// highlightCSS emits class-based Chroma styles for light and dark, so the
// page needs no inline styles (and therefore no CSP relaxation).
func highlightCSS() string {
	f := chromahtml.New(chromahtml.WithClasses(true))
	var light, dark bytes.Buffer
	_ = f.WriteCSS(&light, styles.Get("github"))
	_ = f.WriteCSS(&dark, styles.Get("github-dark"))
	return light.String() + "\n@media (prefers-color-scheme: dark){\n" + dark.String() + "}\n"
}

// ---- note preparation ----

var h1Re = regexp.MustCompile(`^#\s+(.+?)\s*#*\s*$`)

// dropLeadingTitle removes a first-line "# Title" that merely repeats the
// page title, which the template already renders.
func dropLeadingTitle(src, title string) string {
	trimmed := strings.TrimLeft(src, "\r\n")
	line, rest, _ := strings.Cut(trimmed, "\n")
	if m := h1Re.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil &&
		strings.EqualFold(strings.TrimSpace(m[1]), strings.TrimSpace(title)) {
		return rest
	}
	return src
}

func fenceOf(line string) (ch byte, n int) {
	t := strings.TrimLeft(line, " \t")
	if len(t) < 3 || (t[0] != '`' && t[0] != '~') {
		return 0, 0
	}
	ch = t[0]
	for n < len(t) && t[n] == ch {
		n++
	}
	if n < 3 {
		return 0, 0
	}
	return ch, n
}

// stripInlineComments removes %%...%%; an unclosed %% swallows the rest of the
// line and reports that a multi-line comment has started. %% inside inline
// code spans is left alone.
func stripInlineComments(line string) (out string, open bool) {
	var b strings.Builder
	inCode, codeLen := false, 0
	i := 0
	for i < len(line) {
		c := line[i]
		if c == '`' {
			j := i
			for j < len(line) && line[j] == '`' {
				j++
			}
			run := j - i
			if !inCode {
				inCode, codeLen = true, run
			} else if run == codeLen {
				inCode = false
			}
			b.WriteString(line[i:j])
			i = j
			continue
		}
		if !inCode && c == '%' && i+1 < len(line) && line[i+1] == '%' {
			if k := strings.Index(line[i+2:], "%%"); k >= 0 {
				i += 2 + k + 2
				continue
			}
			if strings.HasSuffix(line, "\n") {
				b.WriteByte('\n')
			}
			return b.String(), true
		}
		b.WriteByte(c)
		i++
	}
	return b.String(), false
}

// stripComments removes Obsidian %%comments%% (private notes-to-self) outside
// fenced code. An unterminated comment runs to the end of the document:
// failing closed is safer than leaking it.
func stripComments(s string) string {
	var out strings.Builder
	inFence, inComment := false, false
	var fenceCh byte
	fenceLen := 0
	for _, line := range strings.SplitAfter(s, "\n") {
		if inComment {
			idx := strings.Index(line, "%%")
			if idx < 0 {
				continue
			}
			line = line[idx+2:]
			inComment = false
		} else if inFence {
			out.WriteString(line)
			if ch, n := fenceOf(line); ch == fenceCh && n >= fenceLen && strings.TrimSpace(strings.TrimLeft(line, " \t")[n:]) == "" {
				inFence = false
			}
			continue
		} else if ch, n := fenceOf(line); ch != 0 {
			inFence, fenceCh, fenceLen = true, ch, n
			out.WriteString(line)
			continue
		}
		res, open := stripInlineComments(line)
		out.WriteString(res)
		inComment = open
	}
	return out.String()
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

func describe(renderedHTML string) string {
	t := html.UnescapeString(tagRe.ReplaceAllString(renderedHTML, " "))
	t = strings.Join(strings.Fields(t), " ")
	rs := []rune(t)
	if len(rs) <= 200 {
		return t
	}
	cut := string(rs[:200])
	if i := strings.LastIndex(cut, " "); i > 120 {
		cut = cut[:i]
	}
	return cut + "…"
}

func (r *resolver) renderNote(md goldmark.Markdown, p *pubNote, raw []byte) ([]byte, error) {
	_, body, _ := splitFrontmatter(raw)
	src := dropLeadingTitle(stripComments(string(body)), p.title)
	var content bytes.Buffer
	if err := md.Convert([]byte(src), &content); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	err := pageTmpl.Execute(&out, pageData{
		Title:       p.title,
		Description: describe(content.String()),
		Body:        template.HTML(content.String()),
	})
	return out.Bytes(), err
}
