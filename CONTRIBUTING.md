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
internal/app            operations shared by CLI and GUI (push, pull, remove, migrate)
internal/store          the encrypted git store: clone, sync, format, migration
internal/session        Claude Code's local storage: discovery, bundling, relocation, install
internal/crypto         age encryption and the passphrase-wrapped store key
internal/testutil       test helpers: temp git remotes, simulated machines, fixtures
build/, packaging/      release builds: dist.sh (browser binaries), desktop.sh (native apps), macOS bundle, Linux installer
```

Dependencies point one way: `cli` → `gui` → `app` → `store` → `session`, `crypto`.
Front-ends read listings from `store` and `session` but change state only
through `app`, so the CLI and GUI behave identically.

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
