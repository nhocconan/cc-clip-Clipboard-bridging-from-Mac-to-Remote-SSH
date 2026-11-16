BINARY := cc-clip
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

.PHONY: build test vet clean release-local release-preflight release-notes-check i18n-check

build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/cc-clip/
	@if [ "$$(uname -s)" = "Darwin" ]; then \
		codesign --force --sign - --identifier com.cc-clip.cli $(BINARY); \
	fi

test:
	go test ./... -count=1

vet:
	go vet ./...

clean:
	rm -f $(BINARY)
	rm -rf dist/

# Mirrors everything the `Release` GitHub Actions workflow does before
# invoking GoReleaser, plus a real snapshot build across every target arch.
# Run this before tagging a new release so any drift between the workflow,
# .goreleaser.yaml, or scripts/install.sh surfaces locally instead of after
# a tag push has already burned a version number.
release-preflight: tunnel-lifecycle-check test vet
	@echo "==> cross-compile sanity (6 target triples)"
	@for platform in $(PLATFORMS); do \
		os=$${platform%%/*}; \
		arch=$${platform##*/}; \
		echo "  $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch go build ./... || { echo "FAIL: $$os/$$arch"; exit 1; }; \
	done
	@echo "==> goreleaser check"
	@command -v goreleaser >/dev/null 2>&1 || { \
		echo "goreleaser is not installed. Install with: brew install goreleaser/tap/goreleaser"; \
		exit 1; \
	}
	@goreleaser check
	@echo "==> release contract greps (mirrors .github/workflows/checks.yml)"
	@grep -qE 'name_template:.*ProjectName.*Version.*Os.*Arch' .goreleaser.yaml \
		|| { echo "FAIL: name_template drift in .goreleaser.yaml"; exit 1; }
	@grep -Fq 'cc-clip_$${VERSION#v}_$${PLATFORM}.tar.gz' scripts/install.sh \
		|| { echo "FAIL: scripts/install.sh archive name drift"; exit 1; }
	@grep -Fq '$$archiveName = "cc-clip_$$($$version.TrimStart("v"))_$${platform}.zip"' scripts/install.ps1 \
		|| { echo "FAIL: scripts/install.ps1 archive name drift"; exit 1; }
	@grep -Fq 'formats: [tar.gz]' .goreleaser.yaml \
		|| { echo "FAIL: .goreleaser.yaml no longer declares formats: [tar.gz]"; exit 1; }
	@grep -Fq 'formats: [zip]' .goreleaser.yaml \
		|| { echo "FAIL: .goreleaser.yaml no longer declares the Windows formats: [zip] override"; exit 1; }
	@echo "==> cc-clip update contract greps (keep update.go aligned with above)"
	@grep -Fq 'cc-clip_%s_%s_%s.tar.gz' cmd/cc-clip/update.go \
		|| { echo "FAIL: cmd/cc-clip/update.go archive-name drift; releaseArchiveName() must stay aligned with goreleaser name_template and scripts/install.sh"; exit 1; }
	@grep -Fq 'checksums.txt' cmd/cc-clip/update.go \
		|| { echo "FAIL: cmd/cc-clip/update.go no longer fetches checksums.txt; integrity verification broken"; exit 1; }
	@echo "==> goreleaser snapshot build (no publish)"
	@goreleaser release --snapshot --clean --skip=publish
	@echo "==> release preflight OK. Safe to tag."

release-local: clean
	@mkdir -p dist
	@for platform in $(PLATFORMS); do \
		os=$${platform%%/*}; \
		arch=$${platform##*/}; \
		output=dist/$(BINARY)-$${os}-$${arch}; \
		if [ "$$os" = "windows" ]; then output="$${output}.exe"; fi; \
		echo "Building $$platform..."; \
		GOOS=$$os GOARCH=$$arch go build $(LDFLAGS) -o $$output ./cmd/cc-clip/ || exit 1; \
		if [ "$$os" = "darwin" ] && [ "$$(uname -s)" = "Darwin" ]; then \
			echo "  Signing $$output..."; \
			codesign --force --sign - --identifier com.cc-clip.cli $$output || exit 1; \
		fi; \
	done
	@echo "Binaries in dist/"
	@ls -lh dist/

# Release notes are written before the tag and become the GitHub release body
# (release.yml passes them to goreleaser). Run with the tag you are about to
# push: make release-notes-check V=v0.11.2
release-notes-check:
	@test -n "$(V)" || { echo "usage: make release-notes-check V=vX.Y.Z"; exit 1; }
	@test -s "docs/release-notes/$(V).md" || { \
		echo "FAIL: docs/release-notes/$(V).md is missing or empty."; \
		echo "Write the user-facing notes (see docs/release.md, Phase 2.5) and commit them before tagging."; \
		exit 1; \
	}
	@grep -q "^## What's New in $(V)" "docs/release-notes/$(V).md" || { \
		echo "FAIL: docs/release-notes/$(V).md must start its body with: ## What's New in $(V)"; \
		exit 1; \
	}
	@echo "release notes OK: docs/release-notes/$(V).md"

# Translations start with <!-- i18n-source: <path> @ <sha> -->. Fails when the
# English source changed after that SHA (docs/i18n/TRANSLATION_BRIEF.md).
i18n-check:
	@bash scripts/check-i18n-markers.sh

# Regression gate for safe recovery after VPN/Tailscale blackholes.
tunnel-lifecycle-check:
	bash scripts/audit/tunnel-lifecycle.sh
