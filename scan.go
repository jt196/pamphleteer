package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"

	"gopkg.in/yaml.v3"
)

const (
	maxFrontmatterBytes = 32 << 10
	maxNoteBytes        = 4 << 20
	embedHashLen        = 12
)

var slugRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Slugs that would shadow a fixed route. Every other fixed route contains a
// dot, which the slug charset forbids.
var reservedSlugs = map[string]bool{"healthz": true}

// Attachment types that may be embedded in a published note. Anything else in
// the vault is invisible to the server.
var attachmentTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".avif": "image/avif",
	".svg": "image/svg+xml",
	".mp4": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime",
	".m4v": "video/mp4", ".ogv": "video/ogg",
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg",
	".m4a": "audio/mp4", ".opus": "audio/ogg", ".aac": "audio/aac",
	".flac": "audio/flac",
	".pdf":  "application/pdf",
}

type fileKey struct {
	mtime int64
	size  int64
}

type vaultFile struct {
	path string // absolute
	rel  string // slash-separated, relative to the vault root
	key  fileKey
}

type pubNote struct {
	vaultFile
	slug    string
	title   string
	created string
}

type noteMeta struct {
	key     fileKey
	publish bool
	slug    string
	title   string
	created string
}

type page struct {
	body []byte
	etag string
}

type embedInfo struct {
	path  string
	ctype string
}

// snapshot is immutable once stored; requests only ever read from it.
type snapshot struct {
	pages       map[string]*page
	embeds      map[string]embedInfo
	fingerprint string
}

type hashEntry struct {
	key  fileKey
	hash string
}

type scanner struct {
	vault     string
	log       *slog.Logger
	cur       *atomic.Pointer[snapshot]
	meta      map[string]noteMeta
	hashes    map[string]hashEntry
	showDates bool
}

func newScanner(vault string, log *slog.Logger, cur *atomic.Pointer[snapshot]) *scanner {
	return &scanner{
		vault:     vault,
		log:       log,
		cur:       cur,
		meta:      map[string]noteMeta{},
		hashes:    map[string]hashEntry{},
		showDates: true,
	}
}

type vaultWalk struct {
	notes []vaultFile
	atts  []vaultFile
}

func skipDirName(name string) bool {
	return strings.HasPrefix(name, ".") || name == "@eaDir" || name == "#recycle" || name == "node_modules"
}

// walkVault lists notes and embeddable attachments. It never follows
// symlinks and skips hidden directories (.obsidian, .git, Syncthing's
// .stversions, ...), so stale or foreign copies of a note can't be published.
func walkVault(root string) (*vaultWalk, error) {
	out := &vaultWalk{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && skipDirName(name) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if strings.HasPrefix(name, ".") || strings.Contains(name, ".sync-conflict-") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		_, isAtt := attachmentTypes[ext]
		if ext != ".md" && !isAtt {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		vf := vaultFile{
			path: p,
			rel:  filepath.ToSlash(rel),
			key:  fileKey{mtime: info.ModTime().UnixNano(), size: info.Size()},
		}
		if ext == ".md" {
			out.notes = append(out.notes, vf)
		} else {
			out.atts = append(out.atts, vf)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// splitFrontmatter separates a leading YAML block from the body. ok is false
// when there is no well-formed block, in which case body is the whole input.
func splitFrontmatter(b []byte) (fm, body []byte, ok bool) {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	var rest []byte
	switch {
	case bytes.HasPrefix(b, []byte("---\n")):
		rest = b[4:]
	case bytes.HasPrefix(b, []byte("---\r\n")):
		rest = b[5:]
	default:
		return nil, b, false
	}
	pos := 0
	for {
		nl := bytes.IndexByte(rest[pos:], '\n')
		line, next := rest[pos:], len(rest)
		if nl >= 0 {
			line, next = rest[pos:pos+nl], pos+nl+1
		}
		if t := string(bytes.TrimRight(line, "\r \t")); t == "---" || t == "..." {
			return rest[:pos], rest[next:], true
		}
		if nl < 0 {
			return nil, b, false
		}
		pos = next
	}
}

// parseMeta reads publish/slug/title. publish must be a real YAML boolean
// true: the string "true", "yes" and friends do not count.
func parseMeta(head []byte, stem string) (publish bool, slug, title, created string) {
	title = stem
	fm, _, ok := splitFrontmatter(head)
	if !ok {
		return false, "", title, ""
	}
	var m map[string]yaml.Node
	if err := yaml.Unmarshal(fm, &m); err != nil {
		return false, "", title, ""
	}
	if n, ok := m["publish"]; ok && n.Kind == yaml.ScalarNode && n.ShortTag() == "!!bool" && strings.EqualFold(n.Value, "true") {
		publish = true
	}
	if n, ok := m["slug"]; ok && n.Kind == yaml.ScalarNode {
		slug = n.Value
	}
	if n, ok := m["title"]; ok && n.Kind == yaml.ScalarNode && strings.TrimSpace(n.Value) != "" {
		title = strings.TrimSpace(n.Value)
	}
	if n, ok := m["created"]; ok && n.Kind == yaml.ScalarNode {
		created = n.Value
	}
	return publish, slug, title, created
}

func readHead(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, maxFrontmatterBytes)
	n, _ := io.ReadFull(f, buf)
	return buf[:n]
}

func readMeta(f vaultFile) noteMeta {
	stem := strings.TrimSuffix(filepath.Base(f.path), filepath.Ext(f.path))
	publish, slug, title, created := parseMeta(readHead(f.path), stem)
	return noteMeta{key: f.key, publish: publish, slug: slug, title: title, created: created}
}

func readLimited(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("larger than %d bytes", max)
	}
	return b, nil
}

// validate keeps only notes that are safe to serve: a valid, non-reserved,
// unique slug. Anything ambiguous is dropped rather than guessed at.
func validate(cands []*pubNote) (pubs []*pubNote, problems []string) {
	bySlug := map[string][]*pubNote{}
	for _, c := range cands {
		if !slugRe.MatchString(c.slug) || reservedSlugs[c.slug] {
			problems = append(problems, "publish:true note not served (missing or invalid slug): "+c.rel)
			continue
		}
		bySlug[c.slug] = append(bySlug[c.slug], c)
	}
	for _, group := range bySlug {
		if len(group) > 1 {
			rels := make([]string, len(group))
			for i, g := range group {
				rels[i] = g.rel
			}
			sort.Strings(rels)
			problems = append(problems, "duplicate slug, none of these notes are served: "+strings.Join(rels, ", "))
			continue
		}
		pubs = append(pubs, group[0])
	}
	sort.Slice(pubs, func(i, j int) bool { return pubs[i].rel < pubs[j].rel })
	sort.Strings(problems)
	return pubs, problems
}

func fingerprint(pubs []*pubNote, atts []vaultFile, problems []string) string {
	h := sha256.New()
	for _, p := range pubs {
		fmt.Fprintf(h, "n|%s|%d|%d|%s|%s|%s\n", p.rel, p.key.mtime, p.key.size, p.slug, p.title, p.created)
	}
	for _, a := range atts {
		fmt.Fprintf(h, "a|%s|%d|%d\n", a.rel, a.key.mtime, a.key.size)
	}
	for _, pr := range problems {
		fmt.Fprintf(h, "p|%s\n", pr)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *scanner) hashFile(f vaultFile) (string, error) {
	if e, ok := s.hashes[f.path]; ok && e.key == f.key {
		return e.hash, nil
	}
	fh, err := os.Open(f.path)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))[:embedHashLen]
	s.hashes[f.path] = hashEntry{key: f.key, hash: sum}
	return sum, nil
}

// scan walks the vault and, if anything that affects published output has
// changed, renders every published note into a fresh snapshot and swaps it in.
func (s *scanner) scan() (changed bool, err error) {
	vw, err := walkVault(s.vault)
	if err != nil {
		return false, err
	}

	seen := make(map[string]struct{}, len(vw.notes))
	var cands []*pubNote
	for _, f := range vw.notes {
		seen[f.path] = struct{}{}
		m, ok := s.meta[f.path]
		if !ok || m.key != f.key {
			m = readMeta(f)
			s.meta[f.path] = m
		}
		if m.publish {
			cands = append(cands, &pubNote{vaultFile: f, slug: m.slug, title: m.title, created: m.created})
		}
	}
	for p := range s.meta {
		if _, ok := seen[p]; !ok {
			delete(s.meta, p)
		}
	}

	pubs, problems := validate(cands)
	fp := fingerprint(pubs, vw.atts, problems)
	if cur := s.cur.Load(); cur != nil && cur.fingerprint == fp {
		return false, nil
	}

	for _, p := range problems {
		s.log.Warn(p)
	}
	snap := s.build(pubs, vw.atts, fp)

	attSeen := make(map[string]struct{}, len(vw.atts))
	for _, a := range vw.atts {
		attSeen[a.path] = struct{}{}
	}
	for p := range s.hashes {
		if _, ok := attSeen[p]; !ok {
			delete(s.hashes, p)
		}
	}

	s.cur.Store(snap)
	s.log.Info("index updated", "published", len(snap.pages), "embeds", len(snap.embeds))
	return true, nil
}

func (s *scanner) build(pubs []*pubNote, atts []vaultFile, fp string) *snapshot {
	res := newResolver(pubs, atts, s.hashFile)
	res.showDates = s.showDates
	md := res.markdown()
	snap := &snapshot{pages: make(map[string]*page, len(pubs)), fingerprint: fp}
	for _, p := range pubs {
		raw, err := readLimited(p.path, maxNoteBytes)
		if err != nil {
			s.log.Warn("note not served: read failed", "note", p.rel, "err", err)
			continue
		}
		res.omitted = 0
		body, err := res.renderNote(md, p, raw)
		if err != nil {
			s.log.Warn("note not served: render failed", "note", p.rel, "err", err)
			continue
		}
		if res.omitted > 0 {
			s.log.Warn("raw HTML <img> with a local path was dropped; embed it with ![[file]] instead", "note", p.rel, "images", res.omitted)
		}
		sum := sha256.Sum256(body)
		snap.pages[p.slug] = &page{body: body, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
	}
	snap.embeds = res.embeds
	return snap
}
