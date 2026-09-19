# markdown-publish

Publish individual notes from a Markdown/Obsidian vault at private, unguessable
URLs. One small Go binary in a `scratch` container: no Node, no build step, no
JavaScript sent to visitors.

It is a from-scratch replacement for a Quartz-based stack, and it reads the same
frontmatter that the [Private Quartz Publish](https://github.com/jagajaga/private-quartz-publish)
Obsidian plugin writes, so the plugin keeps working unchanged.

|                | This                                   | Quartz stack it replaces                |
|----------------|----------------------------------------|-----------------------------------------|
| Containers     | 1                                      | 2                                       |
| Image size     | ~19 MB                                 | ~1.5 GB (web) + stager                  |
| Memory (RSS)   | ~23 MB with an 11k-file vault          | ~115 MB by `docker stats`               |
| Change → live  | next scan (default 5 s)                | 3–60 s rebuild                          |
| Client JS      | none                                   | Quartz SPA bundle                       |

## How it works

Notes opt in with frontmatter:

```yaml
---
publish: true        # must be a real boolean
slug: aT3kP9wQ2x     # the URL: /aT3kP9wQ2x  (letters, digits, _ and -)
title: Optional      # defaults to the file name
---
```

A background scan (every `SCAN_INTERVAL`) walks the vault and builds an
immutable in-memory snapshot: `slug → pre-rendered page` and
`hash.ext → attachment`. **Requests only ever look things up in that snapshot;
a request path is never turned into a filesystem path.** Unpublishing or
rotating a slug takes effect on the next scan.

## Run it

`docker-compose.example.yaml` is a ready-to-use hardened service (read-only root
filesystem, all capabilities dropped, non-root user, vault mounted read-only):

```sh
cp docker-compose.example.yaml docker-compose.yaml
cp .env.example .env          # set VAULT_DIR, and PUID/PGID that can read it
docker compose up -d
```

Put a TLS-terminating reverse proxy in front of `PUBLISH_PORT`; the container
speaks plain HTTP. Configuration is via environment variables:

| Variable        | Default  | Meaning                                                  |
|-----------------|----------|----------------------------------------------------------|
| `VAULT_DIR`     | `/vault` | Vault root (mount it read-only)                          |
| `LISTEN`        | `:8080`  | Listen address                                           |
| `SCAN_INTERVAL` | `5s`     | How often to rescan; minimum `1s`                        |

These are the container's own variables. In the compose example, `VAULT_DIR` in
`.env` is the *host* path that gets mounted at `/vault` (the container's
`VAULT_DIR` stays `/vault`).

The image has no shell, so the healthcheck is built in: `/markdown-publish -healthcheck`.

## Privacy behaviour

Everything below is covered by tests (`make test`).

- **Fail closed.** A note is served only with `publish: true` (a YAML boolean;
  `"true"` and `yes` don't count) **and** a valid slug. A missing slug is *not*
  replaced by a guessable file-name slug. Two notes with the same slug: neither
  is served.
- **No stale or foreign copies.** Symlinks are never followed; hidden
  directories (`.obsidian`, `.git`, Syncthing's `.stversions`, ...),
  `node_modules`, Synology `@eaDir`/`#recycle` and `*.sync-conflict-*` files are
  ignored, so an old `publish: true` copy of a note can't come back.
- **Links to unpublished notes become plain text**, never a link or URL.
- **Attachments are served by content hash** (`/<12 hex>.<ext>`), never by their
  vault name or path, and only if a published note embeds them. Unresolvable
  embeds disappear rather than printing a file name.
- **`%%Obsidian comments%%` are stripped** (an unclosed one swallows the rest
  of the note), except inside code. Frontmatter is never rendered.
- **No raw HTML** except a few attribute-free inline tags (`<br>`, `<kbd>`,
  `<sub>`, ...). Block-level HTML is dropped, and a warning naming the note is
  logged.
- **Hardened responses.** CSP `default-src 'none'` with no script source,
  `nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`,
  `noindex` (header, meta and `robots.txt`), uniform uncacheable 404s. No
  third-party requests: fonts are system fonts.

## Supported Markdown

CommonMark + GFM (tables, task lists, strikethrough, autolinks), footnotes,
syntax highlighting (Chroma, light/dark, class-based so no inline styles),
`[[wikilinks]]` / `[[Note|alias]]`, `![[image.png|300]]` embeds, images, and
audio/video via embeds or links. Light/dark follows the visitor's OS setting.

## Not (yet) supported

- Folder bundles / folder sidebars (single notes only).
- Math, callouts, Mermaid, Dataview, popovers, in-page search.
- Block-level raw HTML (e.g. `<details>` on its own line) is omitted.
- `![[Other Note]]` note embeds render as a link, not transcluded content.
- Heading anchors in wikilinks (`[[Note#Heading]]` links to the note).

## Develop

Go isn't needed on the host; everything runs in a pinned `golang` container.

```sh
make test     # go test -race ./...
make vet
make image    # docker build -t markdown-publish:local .
```
