# Build entry point. Its one job beyond `go build` is stamping the version:
# Go records the commit in the build info but not the tag it sits on, so a
# plain build reports a bare SHA. `git describe` is the only thing that knows
# the tag, and it has to be passed in at link time.
#
# VERSION is overridable (`make build VERSION=v1.2.3`) for a build from a
# source archive, where there is no git to ask.
VERSION ?= $(shell git describe --tags --dirty --always 2>/dev/null)

MODULE  := github.com/kelsos/rwt
PREFIX  ?= $(HOME)/.local
BIN     := $(PREFIX)/bin/rwt

# An empty VERSION means no git and no override: link without the flag rather
# than stamping "", so resolveVersion falls back to the build-info revision
# instead of reporting nothing.
ifeq ($(strip $(VERSION)),)
LDFLAGS :=
else
LDFLAGS := -ldflags "-X $(MODULE)/internal/cli.version=$(VERSION)"
endif

.PHONY: build install version test lint clean

## build: compile ./rwt in the working directory
build:
	go build $(LDFLAGS) -o rwt ./cmd/rwt

## install: compile straight into $(PREFIX)/bin
install:
	go build $(LDFLAGS) -o $(BIN) ./cmd/rwt
	@echo "installed $(BIN) ($(if $(VERSION),$(VERSION),from build info))"

## version: print the version a build would stamp
version:
	@echo "$(if $(VERSION),$(VERSION),(none: no git tag and no VERSION override))"

## test: the gates the pre-commit hook runs
test:
	gofmt -l .
	go vet ./...
	go test ./...

clean:
	rm -f rwt
