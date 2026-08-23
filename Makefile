GORELEASER ?= goreleaser
.PHONY: help
help:
	@echo
	@echo "Available targets:"
	@echo "  * build             - build snapshot binaries with goreleaser"
	@echo "  * lint              - run golangci-lint"
	@echo "  * lint-fix          - run golangci-lint with --fix"
	@echo "  * fmt               - run golangci-lint fmt"
	@echo "  * test              - run go test"
	@echo "  * test-race         - run go test -race"
	@echo "  * cover             - run tests with coverage report"
	@echo "  * bench             - run benchmarks"

.PHONY: build lint lint-fix fmt test test-race cover bench
build:
	$(GORELEASER) build --snapshot --clean

# If this fails with "could not import ... (typecheck)" pointing at a stdlib
# path (e.g. math/rand/v2), your local Go toolchain is newer than the one
# your installed golangci-lint was built with - golangci-lint's own type
# checker can't parse newer stdlib syntax yet. Not a code bug: `go build`/`go
# vet` still work. CI is unaffected - it pins Go via go-version-file: go.mod.
# Fix locally by updating golangci-lint or using the Go version go.mod names.
lint:
	golangci-lint run ./...

lint-fix:
	golangci-lint run --fix ./...

fmt:
	golangci-lint fmt ./...

test:
	go test ./...

test-race:
	go test -race ./...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

bench:
	go test -bench=. -benchmem ./...
