# Contributing to cc-clip

Thanks for your interest in contributing to cc-clip!

## Getting Started

```bash
git clone https://github.com/nhocconan/cc-clip-Clipboard-bridging-from-Mac-to-Remote-SSH.git
cd cc-clip-Clipboard-bridging-from-Mac-to-Remote-SSH
make build
make test
```

## Development

**Requirements:** Go 1.25.13 or newer (see `go.mod`), `pngpaste` (macOS, `brew install pngpaste`)

**Project structure:**
- `cmd/cc-clip/` — CLI entry point
- `internal/daemon/` — Local clipboard HTTP server
- `internal/tunnel/` — Client for fetching through SSH tunnel
- `internal/shim/` — Bash shim templates and remote deployment
- `internal/token/` — Session token management
- `internal/exitcode/` — Segmented exit codes
- `internal/doctor/` — Diagnostic checks

**Commands:**
```bash
make build          # Build binary
make test           # Run all tests
make vet            # Run go vet
make release-local  # Cross-compile for local testing only (NOT for GitHub releases)
```

> **Warning:** `make release-local` produces bare binaries with different naming than
> goreleaser. Never upload these to GitHub Releases — the install script will 404.
> Production releases are automated via GitHub Actions on tag push. See CLAUDE.md.

## How to Contribute

### Bug Reports

Open an [issue](https://github.com/nhocconan/cc-clip-Clipboard-bridging-from-Mac-to-Remote-SSH/issues) with:
- cc-clip version (`cc-clip --version`)
- Local OS and remote OS/arch
- Steps to reproduce
- Output of `cc-clip doctor --host <your-host>` (redact sensitive info)
- Shim debug logs: `CC_CLIP_DEBUG=1 xclip -selection clipboard -t TARGETS -o`

### Feature Requests

Open an issue describing the use case and proposed solution.

### Pull Requests

1. Fork the repo and create a feature branch
2. Write tests for new functionality
3. Ensure `make test` and `make vet` pass
4. Use conventional commit messages: `feat:`, `fix:`, `refactor:`, `docs:`, `test:`
5. Keep PRs focused — one feature or fix per PR

### Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Keep files under 400 lines where possible
- Prefer immutability — create new objects rather than mutating
- Add comments for non-obvious logic

## Maintaining CLI compatibility

Before releasing, review changes since the previous release in
[upstream cc-clip](https://github.com/ShunmeiCho/cc-clip/commits/main/),
[Claude Code](https://github.com/anthropics/claude-code/blob/main/CHANGELOG.md),
[Codex CLI](https://learn.chatgpt.com/docs/changelog), and
[Antigravity CLI](https://antigravity.google/docs/changelog?tab=cli).
Record relevant changes and the validation performed in the pull request.

Verify the managed hook configuration and payload contracts, then test image
paste and notifications on a remote Linux host with the CLI versions being
claimed as supported. Unit tests alone do not establish interactive compatibility.
Keep existing user hooks intact and check for duplicate notifications when a CLI
adds native notification support.

CI runs Linux, Windows, and macOS tests on pull requests and weekly. Dependabot
proposes Go module and GitHub Actions updates weekly; updates still need review
and passing checks. Run `make release-preflight` before tagging; see
[the release guide](docs/release.md).

## Coordinated Changes

Some changes require updates across multiple files. See `CLAUDE.md` for the full list:
- New API endpoint → `daemon/server.go` + `tunnel/fetch.go` + `shim/template.go`
- New exit code → `exitcode/exitcode.go` + `cmd/cc-clip/main.go` + shim templates
- Token format change → `token/token.go` + shim templates

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
