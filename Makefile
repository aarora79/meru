# Makefile for Meru's checks. Every target here matches a step in
# .github/workflows/, so `make check` runs locally what CI runs.
# docs/ci.md explains each check.
#
# The analysis tools run through `go run tool@version`: the go command
# downloads and caches them, so nothing needs a global install and every
# machine runs the same version. Bump a version here and CI follows, because
# CI calls these targets.

STATICCHECK_VERSION ?= v0.8.1
GOVULNCHECK_VERSION ?= v1.8.0
GOSEC_VERSION       ?= v2.29.0
ACTIONLINT_VERSION  ?= v1.7.12
GITLEAKS_VERSION    ?= v8.30.1

STATICCHECK := honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
GOSEC       := github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)
ACTIONLINT  := github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)
GITLEAKS    := github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION)

# The platforms `make build` cross-compiles for, as GOOS/GOARCH.
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64

# LDFLAGS goes to the Go linker in `make build` and `make desktop`. It stays
# empty unless scripts/release.sh sets it to stamp the version in.
LDFLAGS ?=

# VERSION and DRY_RUN are for `make release`; docs/releasing.md explains them.
VERSION ?=
DRY_RUN ?=

COVER_PROFILE := coverage.out

# GO_FILES lists every Go file outside hidden directories. gofmt walks every
# directory it is given, and a local checkout can hold agent worktrees under
# .claude/ that belong to other branches.
GO_FILES = $(shell find . -path './.*' -prune -o -name '*.go' -print)

.PHONY: help fmt fmt-check vet lint test cover e2e vuln sec sec-sarif secrets secrets-history tidy-check actionlint build desktop desktop-check desktop-app installer installer-app dmg router-eval pick-eval figures check release clean

help: ## List the targets
	@grep -E '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

fmt: ## Rewrite Go files in gofmt style
	gofmt -w $(GO_FILES)

fmt-check: ## Fail if any Go file isn't gofmt-clean
	@out="$$(gofmt -l $(GO_FILES))"; if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

vet: ## Run go vet
	go vet ./...

lint: ## Run staticcheck
	go run $(STATICCHECK) ./...

test: ## Run unit tests with the race detector
	go test -race -count=1 ./...

cover: ## Run tests with coverage and print the total
	go test -race -count=1 -covermode=atomic -coverprofile=$(COVER_PROFILE) ./...
	go tool cover -func=$(COVER_PROFILE) | tail -n 1

e2e: ## Run the end-to-end tests (build tag e2e)
	@if [ -d test/e2e ]; then \
		go test -race -count=1 -tags e2e ./test/e2e/...; \
	else \
		echo "no test files: test/e2e doesn't exist yet"; \
	fi

router-eval: ## Score the router on labelled questions against the local Ollama
	go test -tags integration -count=1 -v -run TestRouterEval ./internal/router/

pick-eval: ## Score the skill pick on labelled questions against the local Ollama
	go test -tags integration -count=1 -v -run TestPickEval ./internal/agent/

figures: ## Redraw the figures in 100.md, 200.md and ARCHITECTURE.md from the HTML pages (needs Chrome)
	sh docs/architecture/img/render.sh

vuln: ## Report known vulnerabilities in code Meru calls
	go run $(GOVULNCHECK) ./...

sec: ## Run the gosec security linter
	go run $(GOSEC) -quiet ./...

sec-sarif: ## Run gosec and write gosec.sarif for GitHub code scanning
	go run $(GOSEC) -no-fail -fmt sarif -out gosec.sarif ./...

secrets: ## Scan the working tree for committed secrets
	go run $(GITLEAKS) dir --redact --no-banner .

secrets-history: ## Scan every commit for secrets (CI runs this one)
	go run $(GITLEAKS) git --redact --no-banner --verbose .

tidy-check: ## Fail if go.mod or go.sum aren't tidy or don't verify
	go mod verify
	go mod tidy -diff

actionlint: ## Lint the GitHub Actions workflows
	go run $(ACTIONLINT)

# cmd/meru-desktop builds only with -tags desktop, so ./cmd/... here skips
# it and the cross-compile stays free of cgo. `make desktop` builds it.
build: ## Cross-compile ./cmd/... into ./bin/GOOS-GOARCH/
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "build $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$$os-$$arch/ ./cmd/... || exit 1; \
	done

# The desktop app uses Wails v3, which needs cgo and the system's WebView,
# so it builds on, and for, the machine that runs the build. The desktop
# tag takes in cmd/meru-desktop; the production tag turns off Wails'
# development features, the web inspector and the dev-server probe. On
# macOS the C flags match the macOS version the Go linker targets, which
# keeps the linker from warning about each object file.
DESKTOP_TAGS := desktop production
DESKTOP_CGO  := CGO_ENABLED=1
ifeq ($(shell go env GOOS),darwin)
DESKTOP_CGO += CGO_CFLAGS="-O2 -g -mmacosx-version-min=11.0" CGO_LDFLAGS="-mmacosx-version-min=11.0"
endif

desktop: ## Build the desktop app for this machine into bin/meru-desktop (needs cgo)
	$(DESKTOP_CGO) go build -trimpath -tags "$(DESKTOP_TAGS)" -ldflags "$(LDFLAGS)" -o bin/meru-desktop ./cmd/meru-desktop

desktop-check: ## Vet, lint and vuln-check cmd/meru-desktop and cmd/meru-installer with their tags (needs cgo)
	$(DESKTOP_CGO) go vet -tags "$(DESKTOP_TAGS)" ./cmd/meru-desktop ./cmd/meru-installer
	$(DESKTOP_CGO) go run $(STATICCHECK) -tags "$(DESKTOP_TAGS)" ./cmd/meru-desktop ./cmd/meru-installer
	$(DESKTOP_CGO) go run $(GOVULNCHECK) -tags "$(DESKTOP_TAGS)" ./cmd/meru-desktop ./cmd/meru-installer

desktop-app: desktop ## Wrap the desktop app in bin/Meru.app (macOS)
	rm -rf bin/Meru.app
	mkdir -p bin/Meru.app/Contents/MacOS bin/Meru.app/Contents/Resources
	cp cmd/meru-desktop/Info.plist bin/Meru.app/Contents/Info.plist
	cp cmd/meru-desktop/Meru.icns bin/Meru.app/Contents/Resources/Meru.icns
	cp bin/meru-desktop bin/Meru.app/Contents/MacOS/meru-desktop
	@$(STAMP_VERSION) bin/Meru.app/Contents/Info.plist

# STAMP_VERSION writes VERSION, without its v, into an Info.plist, so
# Finder's Get Info shows it. It does nothing when VERSION is empty, as in
# every build but a release.
STAMP_VERSION = stamp() { [ -z "$(VERSION)" ] || { plutil -replace CFBundleShortVersionString -string "$(VERSION:v%=%)" "$$1" && plutil -replace CFBundleVersion -string "$(VERSION:v%=%)" "$$1"; }; }; stamp

# The Mac installer is a second Wails app, built like the desktop app.
# `make installer-app` puts it in "bin/Install Meru.app" with its payload,
# the files it installs, in Contents/Resources/payload: meru and merud for
# Apple silicon, and Meru.app. `make dmg` packs that app in a disk image.
INSTALLER_APP := bin/Install Meru.app
DMG := dist/Meru-$(if $(VERSION),$(VERSION),dev)-macos-arm64.dmg

installer: ## Build the Mac installer for this machine into bin/meru-installer (needs cgo)
	$(DESKTOP_CGO) go build -trimpath -tags "$(DESKTOP_TAGS)" -ldflags "$(LDFLAGS)" -o bin/meru-installer ./cmd/meru-installer

installer-app: desktop-app installer ## Wrap the installer, with meru, merud and Meru.app inside, in "bin/Install Meru.app" (macOS)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/darwin-arm64/ ./cmd/meru ./cmd/merud
	rm -rf "$(INSTALLER_APP)"
	mkdir -p "$(INSTALLER_APP)/Contents/MacOS" "$(INSTALLER_APP)/Contents/Resources/payload"
	cp cmd/meru-installer/Info.plist "$(INSTALLER_APP)/Contents/Info.plist"
	cp cmd/meru-desktop/Meru.icns "$(INSTALLER_APP)/Contents/Resources/Meru.icns"
	cp bin/meru-installer "$(INSTALLER_APP)/Contents/MacOS/meru-installer"
	cp bin/darwin-arm64/meru bin/darwin-arm64/merud "$(INSTALLER_APP)/Contents/Resources/payload/"
	ditto bin/Meru.app "$(INSTALLER_APP)/Contents/Resources/payload/Meru.app"
	@$(STAMP_VERSION) "$(INSTALLER_APP)/Contents/Info.plist"

# hdiutil is the Mac's disk image tool. UDZO is a compressed, read-only
# image, the usual kind for a download. Meru has no Apple developer
# account, so the image and its apps are unsigned, and "Read me first.txt"
# explains the right-click, Open step.
dmg: installer-app ## Pack "Install Meru.app" in dist/Meru-VERSION-macos-arm64.dmg (a Mac with Apple silicon)
	@[ "$$(uname -sm)" = "Darwin arm64" ] || { echo "make dmg runs on a Mac with Apple silicon: Meru.app builds for the machine that builds it"; exit 1; }
	rm -rf dist/dmg "$(DMG)"
	mkdir -p dist/dmg
	ditto "$(INSTALLER_APP)" "dist/dmg/Install Meru.app"
	cp scripts/dmg-readme.txt "dist/dmg/Read me first.txt"
	hdiutil create -quiet -volname "Install Meru" -srcfolder dist/dmg -fs HFS+ -format UDZO -ov "$(DMG)"
	rm -rf dist/dmg
	@ls -lh "$(DMG)"

check: fmt-check vet lint tidy-check test e2e build vuln sec secrets actionlint ## Run every check CI runs, in CI's order

# Releases build on the owner's Mac, not in CI; docs/releasing.md says why.
release: ## Build, pack and publish a release: make release VERSION=v0.4.1 [DRY_RUN=1]
	VERSION="$(VERSION)" DRY_RUN="$(DRY_RUN)" bash scripts/release.sh

clean: ## Remove build, release and coverage output
	rm -rf bin dist $(COVER_PROFILE) gosec.sarif
