# Makefile for the Tendril CLI (Stem)
#
# cmd/stem is part of the root github.com/opentendril/core module — do NOT
# `go mod init` here; a nested go.mod would shadow the root module and break
# every build. Prefer `make sprout` / `make install` from the repo root.

.PHONY: build clean

build:
	go build -o tendril-cli .

clean:
	rm -f tendril-cli
