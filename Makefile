PKG_NAME:=github.com/pauldeng/mosquitto-exporter/cmd/mosquitto-exporter
BUILD_DIR:=bin
MOSQUITTO_EXPORTER_BINARY:=$(BUILD_DIR)/mosquitto_exporter
IMAGE := pauldeng/mosquitto-exporter
# Single source of truth for the version: nearest git tag, falling back to
# the short commit hash when none exists yet. There is no main.GITCOMMIT
# variable to set - the binary reads the commit from
# runtime/debug.ReadBuildInfo() instead - so only main.Version is injected.
VERSION:=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS=-s -w -X main.Version=$(VERSION)
.PHONY: help
help:
	@echo
	@echo "Available targets:"
	@echo "  * build             - build the binary, output to $(MOSQUITTO_EXPORTER_BINARY)"
	@echo "  * linux             - build the binary, output to $(MOSQUITTO_EXPORTER_BINARY)"
	@echo "  * docker            - build docker image"
	@echo "  * lint              - run golangci-lint"
	@echo "  * lint-fix          - run golangci-lint with --fix"
	@echo "  * fmt               - run golangci-lint fmt"
	@echo "  * test              - run go test"
	@echo "  * test-race         - run go test -race"
	@echo "  * cover             - run tests with coverage report"
	@echo "  * bench             - run benchmarks"

.PHONY: build
build: export CGO_ENABLED=0
build:
	@mkdir -p $(BUILD_DIR)
	go build -trimpath -o $(MOSQUITTO_EXPORTER_BINARY) -ldflags="$(LDFLAGS)" $(PKG_NAME)

linux: export GOOS=linux
linux: build

# The Dockerfile expects a prebuilt binary named exactly "mosquitto-exporter"
# at the build context root (goreleaser's own convention, and what its
# per-platform dockers step provides at release time) - .dockerignore excludes
# bin/, so linux's bin/mosquitto_exporter is copied into place, not COPYed
# from bin/ directly.
docker: linux
	cp $(MOSQUITTO_EXPORTER_BINARY) mosquitto-exporter
	docker build -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .
	rm -f mosquitto-exporter

push:
	docker push $(IMAGE):$(VERSION)
	docker push $(IMAGE):latest

.PHONY: lint lint-fix fmt test test-race cover bench
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
