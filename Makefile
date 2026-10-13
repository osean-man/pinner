# Build and install from source:  make install   (or: sudo make install)
PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin
GOFLAGS_BUILD := -trimpath

.PHONY: build server install test lint snapshot clean

build:
	CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -o pinner .

server:
	CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -o pinner-server ./cmd/pinner-server

# Copies over an existing binary in place (works when you own the file even if you don't own the
# directory); otherwise run `sudo make install`.
install: build
	@mkdir -p "$(BINDIR)"
	cp pinner "$(BINDIR)/pinner"
	chmod 0755 "$(BINDIR)/pinner"
	@echo "Installed $$("$(BINDIR)/pinner" --version)"
	@command -v pinner >/dev/null && [ "$$(command -v pinner)" != "$(BINDIR)/pinner" ] && \
		echo "Note: 'pinner' on your PATH is $$(command -v pinner), not $(BINDIR)/pinner" || true

test:
	go test -race ./...

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

# Build every release artifact locally without publishing (needs Docker).
snapshot:
	docker run --rm -u "$$(id -u):$$(id -g)" -e HOME=/tmp -e GOPATH=/tmp/go -e GOCACHE=/tmp/gocache \
		-v "$$PWD":/src -w /src --entrypoint sh goreleaser/goreleaser:v2.18.3 \
		-c 'git config --global --add safe.directory /src && goreleaser release --snapshot --clean'

clean:
	rm -rf pinner pinner-server dist
