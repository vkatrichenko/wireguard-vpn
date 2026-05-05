# Makefile for the wireguard-dashboard Go binary.
#
# `build` cross-compiles a static linux/amd64 binary so it runs on the EC2
# host regardless of where the build is invoked (M-series Mac, GH Actions
# linux runner, etc.). CGO is disabled to avoid pulling in glibc.
#
# `run` is for local laptop dev only — defaults LISTEN_ADDR to 127.0.0.1:8080
# because the production default (172.16.15.1) isn't bindable off the EC2.
# The `$$` in the recipe escapes a literal `$` past Make so the SHELL sees
# `$LISTEN_ADDR` and `${LISTEN_ADDR:-127.0.0.1:8080}` resolves it (or applies
# the default) at runtime.

.PHONY: build run test

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o wireguard-dashboard ./cmd/wireguard-dashboard

run:
	LISTEN_ADDR=$${LISTEN_ADDR:-127.0.0.1:8080} go run ./cmd/wireguard-dashboard

test:
	go test ./...
