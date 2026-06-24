# Makefile for the wireguard-dashboard Go binary.
#
# `build` cross-compiles a static linux/amd64 binary so it runs on the EC2
# host regardless of where the build is invoked (M-series Mac, GH Actions
# linux runner, etc.). CGO is disabled to avoid pulling in glibc.
#
# Build-time metadata is injected via `-ldflags -X main.<Var>=<value>` into
# the package-level `BuildSHA`, `BuildTime`, and `GoVersion` vars declared
# in cmd/wireguard-dashboard/main.go. The About-tab handler renders these
# back to the operator so a deployed binary can be matched to its commit
# without shelling onto the host. `git rev-parse` is guarded with
# `|| echo unknown` so a tarball / non-git checkout still builds cleanly
# (the same sentinel the var defaults provide).
#
# `run` is for local laptop dev only — defaults LISTEN_ADDR to 127.0.0.1:8080
# because the production default (172.16.15.1) isn't bindable off the EC2.
# The `$$` in the recipe escapes a literal `$` past Make so the SHELL sees
# `$LISTEN_ADDR` and `${LISTEN_ADDR:-127.0.0.1:8080}` resolves it (or applies
# the default) at runtime.

.PHONY: build run test

# RELEASE_TAG is the published version (vX.Y.Z) the CI release workflow injects
# from GITHUB_REF_NAME. Locally it defaults to "dev" — an untagged working tree
# isn't a release, so "dev" is the honest label (override with `make build
# RELEASE_TAG=v1.2.3` to mimic a release build).
RELEASE_TAG ?= dev
BUILD_SHA   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME  := $(shell date -u +%FT%TZ)
GO_VERSION  := $(shell go env GOVERSION)

LDFLAGS := -s -w \
	-X main.ReleaseTag=$(RELEASE_TAG) \
	-X main.BuildSHA=$(BUILD_SHA) \
	-X main.BuildTime=$(BUILD_TIME) \
	-X main.GoVersion=$(GO_VERSION)

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o wireguard-dashboard ./cmd/wireguard-dashboard

run:
	LISTEN_ADDR=$${LISTEN_ADDR:-127.0.0.1:8080} go run ./cmd/wireguard-dashboard

test:
	go test ./...
