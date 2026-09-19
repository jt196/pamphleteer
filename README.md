# Pamphleteer

[![CI](https://github.com/jt196/pamphleteer/actions/workflows/build.yml/badge.svg)](https://github.com/jt196/pamphleteer/actions/workflows/build.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Publish individual Markdown notes at private, unguessable URLs, straight from a
folder of notes you already have.**

Pamphleteer is one small Go program in a ~20 MB container: no database, no build
step, no JavaScript sent to visitors. It is built for [Obsidian](https://obsidian.md)
vaults, but it reads any folder of Markdown files.

A note published this way is a *pamphlet*: one page, handed to whoever you give
the link to. It is **not** a website and it is **not** behind a login. Please read
the [security model](#security-model) before publishing anything you care about.

## Is this for you?

**A good fit** if you want to share a note with a friend, a client or a forum
thread without exporting a PDF; you like self-hosting; and "anyone with the link
can read it" is acceptable for what you share.

**Not a good fit** if you need real access control (logins, passwords, expiring
links; see [What's not built](#whats-not-built)), you don't want to run a server
(use [Obsidian Publish](https://obsidian.md/publish) instead), or you want a
whole browsable site of many notes (look at [Quartz](https://quartz.jzhao.xyz/)).

## How it works

```
Obsidian + Private Quartz Publish plugin        you right-click a note: "Publish to web"
        │  writes `publish: true` and `slug: …` into the note's frontmatter
        ▼
your sync (Syncthing, git, rsync, …)  ──►  the vault folder on your server
                                                 │  mounted read-only
                                                 ▼
                                         Pamphleteer container
                                                 │  plain HTTP
                                                 ▼
                              reverse proxy (TLS)  ──►  https://notes.example.com/<slug>
```

Every few seconds Pamphleteer scans the vault, finds notes with `publish: true`
and a valid `slug`, renders them, and serves each at `/<slug>`. Nothing else in
the vault is reachable. Requests are answered from an in-memory index: **a request
path is never turned into a filesystem path.**

## Requirements

- A machine that can run Docker: a NAS, a VPS, a Raspberry Pi. The image is
  built for `amd64` and `arm64`.
- Your vault as ordinary files on that machine, for example synced with
  [Syncthing](https://syncthing.net/), git or rsync.
- A domain and a reverse proxy that provides HTTPS (Caddy, nginx, Traefik,
  Nginx Proxy Manager, ...). The container speaks plain HTTP only.
- Recommended: the [Private Quartz Publish](https://github.com/jagajaga/private-quartz-publish)
  plugin for Obsidian (see [Acknowledgements](#acknowledgements)). You can also
  publish without it, see [Using it](#using-it).

## Quick start

**1. Get your vault onto the server** and note the folder that contains your notes,
for example `/srv/vault`. Mount the vault folder itself, not a parent folder full
of unrelated files: the scanner walks everything under the mount.

**2. Run Pamphleteer.** The user must be able to read the vault.

```sh
docker run -d --name pamphleteer --restart unless-stopped \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  --user 1000:1000 \
  -e TZ=UTC \
  -v /srv/vault:/vault:ro \
  -p 8080:8080 \
  ghcr.io/jt196/pamphleteer:latest

curl -i http://localhost:8080/healthz      # -> 200 ok
```

`latest` follows the newest build. For anything you rely on, **pin a release**
(for example `ghcr.io/jt196/pamphleteer:0.1.0`) and update on purpose; see the
[releases](https://github.com/jt196/pamphleteer/releases) for what changed.

Prefer Compose? Copy [`docker-compose.example.yaml`](docker-compose.example.yaml)
to `docker-compose.yaml` and [`.env.example`](.env.example) to `.env`, set
`VAULT_DIR` (the vault's path on the host) and `PUID`/`PGID`, then
`docker compose up -d`.

**3. Put a reverse proxy in front for HTTPS.** With Caddy, for example:

```
notes.example.com {
    reverse_proxy localhost:8080
}
```

Request URLs contain the secret slugs, so avoid logging them (`access_log off;`
in nginx; Caddy doesn't log requests unless you enable it).

**4. Install the plugin** in Obsidian (Settings, Community plugins, search for
*Private Quartz Publish*), and set its **Base URL** to your address, for example
`https://notes.example.com`. The plugin only uses this to build the link it copies
for you; it never contacts your server.

**5. Publish.** Right-click a note, choose **Publish to web**, and paste the
copied link into a browser. It appears once your sync has delivered the note, plus
at most one scan interval (5 seconds by default).

## Using it

| Action | In Obsidian (with the plugin) | What happens |
| --- | --- | --- |
| Publish | Right-click, *Publish to web* | Sets `publish: true` and a random `slug`, copies the URL |
| Edit | Just edit the note | Live after the next sync and scan |
| Unpublish | Right-click, *Unpublish from web* | Removes `publish`, keeps the slug. The URL returns 404; republishing brings the same URL back |
| Rotate the link | Right-click, *Rotate public URL* | New random slug. The old URL returns 404, so use this if a link went somewhere it shouldn't |

**Without the plugin**, add two fields to the note's frontmatter:

```yaml
---
publish: true            # must be a real YAML boolean, not "true"
slug: 6f1c9ad2e07b4a35   # letters, digits, _ and -, up to 64 characters
---
```

Make the slug long and random, for example `openssl rand -hex 8`. Notes with
`publish: true` but no valid, unique slug are **not** served (Pamphleteer never
invents a slug from the file name), and a warning is logged.

An optional `created:` date and `title:` are also used; the title defaults to the
file name.

## Configuration

| Variable        | Default  | Meaning                                                  |
|-----------------|----------|----------------------------------------------------------|
| `VAULT_DIR`     | `/vault` | Vault root inside the container (mount it read-only)     |
| `LISTEN`        | `:8080`  | Listen address                                           |
| `SCAN_INTERVAL` | `5s`     | How often to rescan the vault; minimum `1s`              |
| `SHOW_DATES`    | `true`   | Show the "Created / Updated" line under the title        |
| `TZ`            | `UTC`    | Time zone for those dates, e.g. `Europe/London`          |

In the Compose example, `VAULT_DIR` in `.env` is the *host* path that is mounted at
`/vault`; the container's own `VAULT_DIR` stays `/vault`.

The image has no shell, so the health check is built in:
`/pamphleteer -healthcheck`. Update with
`docker compose pull && docker compose up -d`.

## What you get

- Notes rendered from CommonMark and GitHub-flavoured Markdown (tables, task
  lists, strikethrough, autolinks), footnotes, and syntax highlighting.
- Obsidian syntax: `[[wikilinks]]` and `[[Note|alias]]`, `![[image.png|300]]`
  embeds. Links to other published notes work; links to unpublished notes become
  plain text.
- Images, audio, video and PDFs embedded from your vault. Supported types: png,
  jpg/jpeg, gif, webp, avif, svg, mp4, webm, mov, m4v, ogv, mp3, wav, ogg, m4a,
  opus, aac, flac, pdf.
- A **contents list** for notes with three or more headings: a sticky panel beside
  the text on wide screens, a collapsed box on phones.
- **Heading permalinks**, and a **Created / Updated** line under the title
  (*Updated* is the file's modified time; *Created* comes from the `created:`
  frontmatter; switch it off with `SHOW_DATES=false`).
- Light and dark themes that follow the visitor's system setting, and a **print
  stylesheet** that gives a clean PDF.
- Raw HTML such as `<details>` and tables, passed through a strict allowlist.

All of it is plain HTML and CSS. No JavaScript is served.

## Security model

Pamphleteer's protection is mostly **obscurity**: an unguessable URL. That is
useful, cheap and easy to reason about, but it is not authentication, and you
should know exactly what it does and doesn't give you.

### The core idea: the link is the key

**Anyone who has a note's URL can read it.** There is no login and no password.
With the plugin's default of 10 random characters from a 62-character alphabet,
a slug carries about 60 bits of randomness, so guessing one is impractical. Treat
a link like a copied house key.

### Obscurity measures (defence in depth, not access control)

- **Nothing is enumerable:** no index page, listing, sitemap or search.
  `robots.txt` disallows everything and every response carries `noindex`, though
  that only stops well-behaved crawlers.
- **One 404 for everything.** A slug that never existed, an unpublished note and a
  deleted note return byte-identical responses, so nobody can tell whether a link
  ever worked.
- **Attachments are never served by name.** They appear only at
  `/<12 hex characters of the file's SHA-256>.<ext>`, and only if a published note
  embeds them, so a file name or vault path never appears in a URL or a page.
- **No vault structure leaks:** links to unpublished notes are plain text with no
  URL, `%%Obsidian comments%%` are stripped, and frontmatter is never rendered.
- **Quiet on the wire:** no third-party requests are made by the page (system
  fonts, no analytics), `Referrer-Policy: no-referrer` stops the URL leaking when
  a visitor follows a link out, and the server keeps no access log.

### Hard guarantees (enforced in code and covered by tests, not obscurity)

- **Fail-closed publishing.** A note is served only with `publish: true` as a real
  boolean **and** a valid, unique slug. Duplicate slugs: neither note is served.
- **No stale or foreign copies.** Symlinks are never followed. Hidden directories
  (`.obsidian`, `.git`, Syncthing's `.stversions`, ...), `node_modules`, Synology
  `@eaDir`/`#recycle` and `*.sync-conflict-*` files are ignored, so an old
  `publish: true` copy of a note can't come back.
- **No path traversal.** Requests are answered from an in-memory index and never
  become file paths.
- **Sanitized output.** Raw HTML goes through an allowlist ([bluemonday](https://github.com/microcosm-cc/bluemonday)):
  no scripts, styles, event handlers or relative URLs. The Content-Security-Policy
  allows no scripts at all.
- **Read-only, minimal container.** The vault is mounted read-only, and the image
  is a single static binary on `scratch` (no shell, no OS) that runs as a non-root
  user, with a read-only root filesystem and no capabilities.

### What this does NOT protect against

- **Anyone who has the link.** Forwarding, chat and email history, browser
  history, screenshots and shared devices all defeat it.
- **Link-preview bots.** When you paste a link into Slack, Discord, iMessage and
  similar, their servers fetch the page and keep a preview (the title and roughly
  the first 200 characters).
- **Your reverse proxy and CDN.** They see every URL, slug included, and often log
  them. Pamphleteer keeps no logs, but its neighbours may.
- **Weak slugs you choose.** `slug: about` is served at `/about`. Slugs from the
  plugin are random and fine; short hand-picked ones are not.
- **Revocation isn't retroactive.** Unpublishing or rotating makes the link 404
  within a scan interval, but anyone who already copied the content keeps it.
- **Names you link to.** If a published note links to an unpublished note, that
  note's name still appears as plain text.
- **Embedded remote media.** An image or video you include by external URL is
  fetched by the visitor's browser from that host, which can see the visitor's IP.
- **Edit timing.** The *Updated* date reveals when a note last changed
  (`SHOW_DATES=false` hides it).
- **The network path.** HTTPS is your reverse proxy's job.
- **No timed access, passwords or accounts.** They aren't built (see below).

**Do not use Pamphleteer for secrets, credentials, or anything whose exposure
would be serious.** If you need real access control, use software with
authentication.

This code was written with AI assistance and has **not had an independent
security review** (see [AI disclosure](#ai-disclosure)). It ships with an
adversarial test suite (`make test`), and dependencies are checked with
`govulncheck`, but that is not the same as an audit. Report problems privately
via [SECURITY.md](SECURITY.md).

## What's not built

These came up during design and were deliberately left out for now:

- Timed (expiring) access, password-protected notes, and per-recipient links.
  The plugin has no interface for them, so they would need either hand-edited
  frontmatter or a fork of the plugin.
- Folder publishing as a bundle with a sidebar: each note in a published folder is
  served individually.
- Math, callouts, Mermaid and Dataview.
- `![[Other Note]]` note embeds render as a link rather than embedding the text.
- A `[[Note#Heading]]` link goes to the note, not the heading.
- Image resizing: images are served exactly as they are in your vault.

Issues and pull requests are welcome; see [Contributing](#contributing).

## Troubleshooting

| Symptom | Check |
| --- | --- |
| A published note returns 404 | `publish` is a real boolean (not `"true"`, not `yes`); there is a `slug` using only letters, digits, `_` and `-`; no other note shares it; the file has reached the server; wait one scan interval; then `docker logs pamphleteer`, which warns about missing, invalid and duplicate slugs |
| Container exits at startup with `initial vault scan failed` | The mount path is wrong, or the container user (`--user` / `PUID`/`PGID`) can't read the vault |
| Slow first start | The first scan reads every note's frontmatter. Mount the vault folder itself, not a parent that contains lots of unrelated files |
| An image doesn't show | The embedded file must exist in the vault (outside hidden folders) and be a supported type; HEIC and TIFF aren't supported. A raw `<img src="local/path.png">` is dropped and logged: use `![[file.png]]` |
| A link to another note isn't clickable | That note isn't published (by design), or its slug is invalid |
| Dates are a day out | Set `TZ` (for example `Europe/London`); the default is UTC |
| A chat app shows an old preview | Those services cache previews; there's nothing to fix on the server |
| Reporting a bug | Include the version: `docker logs pamphleteer` shows it on the first line, or run `docker run --rm ghcr.io/jt196/pamphleteer -version` |
| A very large note isn't served | Notes over 4 MB, or with more than 32 KB of frontmatter, aren't served |

## Footprint

Measured on my own hardware and a synthetic 11,000-file vault, so treat these as a
guide: the image is about 20 MB, the process uses about 20 MB of RAM, and a rescan
of an unchanged vault takes about 24 ms. It replaced a two-container
Quartz-based setup of mine that used roughly 115 MB of RAM and a 1.5 GB image.

## Development

Go isn't needed on your machine; everything runs in a pinned `golang` container.

```sh
make test     # go test -race ./...
make vet
make image    # docker build -t pamphleteer:local .
```

The code is small: `scan.go` (vault scanning and the index), `render.go` and
`features.go` (Markdown, wikilinks, sanitizing, contents, dates), `server.go`
(HTTP), `main.go`.

## Contributing

This is a small hobby project maintained in spare time, so replies may be slow.
Bug reports and pull requests are welcome. The privacy guarantees are the point of
the project, so changes need tests, especially anything touching what gets served.

## Acknowledgements

**Thank you to [Private Quartz Publish](https://github.com/jagajaga/private-quartz-publish)**
by Arseniy Seroka ([@jagajaga](https://github.com/jagajaga)), MIT licensed
([plugin page](https://community.obsidian.md/plugins/private-quartz-publish)).
Its Obsidian plugin provides the entire publishing experience that Pamphleteer
relies on (right-click *Publish to web*, random slugs, rotate and unpublish), and
the `publish` / `slug` frontmatter convention it writes is the contract Pamphleteer
reads. Its reference server (a Deno stager, Quartz and Caddy) also shaped this
design: only notes flagged `publish: true` leave the vault, embeds get hashed names,
and links to unpublished notes are stripped. Pamphleteer is an independent
reimplementation and includes no code from the plugin or its server, but it
wouldn't exist without them. If you use Pamphleteer, please go and thank them.

Pamphleteer is built on some excellent open-source libraries:
[goldmark](https://github.com/yuin/goldmark), [Chroma](https://github.com/alecthomas/chroma),
[bluemonday](https://github.com/microcosm-cc/bluemonday) and
[yaml.v3](https://github.com/go-yaml/yaml). Their licenses are reproduced in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md), which is also shipped inside the
container image at `/licenses/`.

*Pamphleteer is not affiliated with or endorsed by Obsidian or by the authors of
Private Quartz Publish.*

## AI disclosure

Pamphleteer was designed and written in collaboration with
[Claude](https://www.anthropic.com/claude) (an AI model from Anthropic, used through
Claude Code). That includes the Go code, the tests, the container setup and this
documentation. The maintainer, [jt196](https://github.com/jt196), directed the
design and the decisions, and runs it on their own vault. Commits made with AI
assistance carry a `Co-Authored-By` trailer.

AI-written code can contain mistakes, including subtle security ones. It has been
tested (an adversarial test suite, mutation checks that deliberately weaken the
security controls to confirm the tests catch it, `govulncheck`, and visual checks in
a real browser), but it has **not** been independently reviewed by a human security
expert. Read the [security model](#security-model) and decide for yourself.

## License

[MIT](LICENSE). Third-party licenses are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
