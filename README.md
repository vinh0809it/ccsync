# ccsync

Sync Claude Code CLI sessions between machines through an **end-to-end-encrypted
git repository**. Even on a public repo, nobody without the passphrase can read
anything — session content, titles, project names, and paths are all encrypted
(the only visible things are random session UUIDs and commit timestamps).

## How it works

- A session in Claude Code is one file: `~/.claude/projects/<path-slug>/<session-id>.jsonl`.
- `ccsync push` encrypts it (age, scrypt passphrase) together with a metadata
  blob and pushes both to your store repo.
- `ccsync pull --to <dir>` decrypts it on another machine, rewrites the
  structural `cwd` fields to the new project path, and installs it under the
  correct slug so plain `claude --resume <id>` just works — even when the
  project lives at a completely different path or OS.
- Git authentication is **never** handled or stored by ccsync: it shells out to
  your system `git`, reusing your existing SSH key or credential helper.

## Setup

```sh
go build -o ccsync .

# on every machine (same repo, same passphrase):
ccsync init git@github.com:you/my-session-store.git
```

## Usage

```sh
ccsync sessions                  # list local sessions of the current project
ccsync push <id> [<id>...]       # push selected sessions
ccsync push --all                # push all (skips the currently active one)
ccsync ls                        # list what's in the store

# on the other machine:
ccsync pull <id> --to ~/dev/myproject
cd ~/dev/myproject && claude --resume <id>
```

## GUI

```sh
ccsync gui                 # opens the browser UI (setup, unlock, push, pull)
ccsync gui --port 8931 --no-open
```

The GUI is served by the same binary on `127.0.0.1` only. Every API request
requires a random per-run token embedded in the URL it prints, so other local
users can't drive it. The passphrase is held in memory for the run and never
written to disk. First open shows setup (or unlock); after that: pick a
project, tick sessions, push — or pull any store session to a directory you
choose (pre-filled with its original path).

## Building distributable binaries

```sh
./build/dist.sh           # browser-mode binaries for all platforms -> dist/
./build/dist.sh desktop   # also the Linux native-window binary
```

This produces, in `dist/`:

| file | runs on | UI |
|---|---|---|
| `ccsync-<v>-linux-amd64` / `-arm64` | any Linux + git | browser |
| `ccsync-<v>-windows-amd64.exe` | any Windows + git | browser (icon embedded) |
| `ccsync-<v>-darwin-amd64` / `-arm64` | any Mac + git | browser |
| `ccsync-<v>-linux-amd64-desktop` | Linux + webkit2gtk | native window |

**Browser-mode binaries are pure Go (no CGO): one file, no dependencies but
git.** Running `ccsync gui` starts the local server and opens the UI in the
person's default browser. This is the format to hand to another machine.

**Native-window binaries use CGO** (WebKit), so they must be built on the
target OS and need its WebView runtime. The Linux one is built here; Windows
(WebView2) and macOS (WKWebView) native builds need a build on that OS — see
the CI note below.

### Giving it to another computer to test

- **Another Linux PC (native window):** `./build/dist.sh desktop`, copy the
  repo (or just the `-desktop` binary + `assets/`), and run
  `./packaging/linux/install.sh` there — it installs the binary, icon, and an
  app-menu launcher under `~/.local` (no root). Needs `webkit2gtk` + `git`.
- **Windows / Mac / any Linux (browser):** copy the matching `dist/` binary.
  They run `ccsync init <repo-url>` once (or set up in the GUI), then
  `ccsync gui`. Zero install, only git required.
- Everyone uses the **same repo URL and the same passphrase**; each machine
  needs its own git access to that repo (SSH key or credential helper).

### App icon

`assets/icon.svg` is the source. `build/dist.sh` embeds `assets/icon.ico`
into the Windows `.exe` (via `goversioninfo`); the Linux installer uses
`assets/icon-256.png`; the browser tab uses an inline SVG favicon. Regenerate
raster icons with:

```sh
magick -background none assets/icon.svg -resize 256x256 assets/icon-256.png
magick -background none assets/icon.svg -define icon:auto-resize=16,32,48,64,128,256 assets/icon.ico
```

### Native Windows/macOS windows (later)

A GitHub Actions matrix (`ubuntu`/`windows`/`macos`) can build the `-tags
desktop` binary on each OS and attach all three native builds to a release —
the standard way to ship a CGO desktop app. Not set up yet.

## Tests

```sh
go test ./...
```

Covers: crypto round-trip and wrong-passphrase rejection; slug derivation,
metadata extraction, structural-only `cwd` rewriting, and install semantics;
store init/push/list/get against a real bare git remote, a two-machine
round-trip, and a "nothing readable in the repo" leak check; plus a full
GUI API flow (setup → push → pull → on-disk verification) over HTTP.

`--project <dir>` targets a project other than the current directory.
The passphrase is prompted, or read from `$CCSYNC_PASSPHRASE` for scripting.
`CCSYNC_CONFIG_DIR` / `CCSYNC_STORE_DIR` override the default locations
(`~/.config/ccsync`, `~/.local/share/ccsync/store`) — useful for testing.

## Store layout

```
ccsync.json                      plaintext marker {format: 1}
check.age                        encrypted known value → passphrase verification
sessions/<uuid>/data.age         encrypted transcript
sessions/<uuid>/meta.age         encrypted metadata (title, original cwd, host, ...)
```

## Design notes / caveats

- The `.jsonl` transcript format is **internal to Claude Code** and can change
  between versions. ccsync treats it as an opaque blob; only the exact
  `"cwd":"..."` substring is rewritten on pull. Absolute paths inside message
  *content* are intentionally left alone — `--resume` doesn't need them.
- Sessions modified in the last 30 s are treated as live and skipped by
  `push --all` (Claude Code may still be appending).
- Never resume the *same* session on two machines at once and push both — last
  push wins per file.
- Keep Claude Code versions roughly in sync across machines; `ccsync ls` shows
  which version wrote each session.

## Roadmap

- [x] GUI (local web UI served by the binary): checkbox picker for push/pull
- [ ] Native window (Wails wrapper around the same web UI)
- [ ] OS keychain caching of the passphrase
- [ ] Setup export/import (repo URL + preferences — never credentials)
- [ ] Conflict-aware push (auto-retry after `git pull --rebase`)
