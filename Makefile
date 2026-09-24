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

COVER_PROFILE := coverage.out

# GO_FILES lists every Go file outside hidden directories. gofmt walks every
# directory it is given, and a local checkout can hold agent worktrees under
# .claude/ that belong to other branches.
GO_FILES = $(shell find . -path './.*' -prune -o -name '*.go' -print)

.PHONY: help fmt fmt-check vet lint test cover e2e vuln sec sec-sarif secrets secrets-history tidy-check actionlint build router-eval check clean

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

build: ## Cross-compile ./cmd/... into ./bin/GOOS-GOARCH/
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "build $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -o bin/$$os-$$arch/ ./cmd/... || exit 1; \
	done

check: fmt-check vet lint tidy-check test e2e build vuln sec secrets actionlint ## Run every check CI runs, in CI's order

clean: ## Remove build and coverage output
	rm -rf bin $(COVER_PROFILE) gosec.sarif
