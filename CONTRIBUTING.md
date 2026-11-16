# Contributing

Bug reports and pull requests are welcome. For anything larger than a fix,
open an issue first so we can agree on the approach.

## Development

Requirements: Go (see `go.mod`) and `git`.

```sh
go build .            # build ./ccsync
go test ./...         # all tests (they create temp git remotes; no network)
gofmt -l .            # must print nothing
go vet ./...
```

CI runs the same checks plus `go test -race` and `govulncheck`.

## Layout

```
main.go                 entry point; stamps the version
internal/cli            command line: parsing, prompts, output
internal/gui            local web UI: HTTP API (server.go, handlers.go) and index.html
internal/app            operations shared by CLI and GUI: Stores (add, unlock,
                        forget), Client (push, pull, remove) — the fast-forward
                        rules and the store lock live here
internal/store          an encrypted store: format, sessions, migration, the
                        registry of stores, and its transports (git.go,
                        transport.go: synced folder), process lock
internal/session        Claude Code's local storage: discovery, bundling,
                        comparing copies, relocation, install
internal/crypto         age encryption and the passphrase-wrapped store key
internal/github         creating private repos and checking visibility via gh
internal/testutil       test helpers: temp git remotes, simulated machines, fixtures
build/, packaging/      release builds: dist.sh (browser binaries), desktop.sh
                        (native apps), macOS bundle, Linux installer
```

Dependencies point one way: `cli` → `gui` → `app` → `store` → `session` →
`crypto`, with `github` used by `app`. Front-ends read listings but change
state only through `app`, so the CLI and GUI behave identically.

A new kind of store (S3, WebDAV, ...) is a new `transport` in
`internal/store`: the store's files and rules stay the same; only how a
directory of them is shared changes.

## Ground rules

- **Never weaken the privacy guarantee.** Nothing derived from session
  content, titles, paths or hostnames may reach the store unencrypted —
  including commit messages. `TestNothingReadableInRemote` guards this.
- **Treat the store as untrusted input.** Ids, bundle paths and metadata come
  from other machines; validate before they touch the filesystem.
- **Claude Code's formats are not ours.** Read transcripts defensively and
  rewrite only what is documented in the README.
- **A change to the store format** needs a format bump in `store.Format`, a
  migration path, and a CHANGELOG entry.
- Add a test that fails without your fix.
