# Simple Makefile for building whatsapp-mcp with SQLite FTS5 support

BINARY := bin/whatsapp-mcp
PKG    := ./cmd/whatsapp-mcp
TAGS   := sqlite_fts5

.PHONY: build run clean tidy

build:
	@mkdir -p bin
	CGO_ENABLED=1 go build -tags "$(TAGS)" -o $(BINARY) $(PKG)

run: build
	./$(BINARY)

clean:
	rm -f $(BINARY)

tidy:
	go mod tidy


