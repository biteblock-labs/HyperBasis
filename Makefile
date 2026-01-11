BINARY := hl-carry-bot
TOOLS_BIN ?= $(shell go env GOPATH)/bin
STATICCHECK_BIN := $(TOOLS_BIN)/staticcheck
DEADCODE_BIN := $(TOOLS_BIN)/deadcode

.PHONY: build test run ci vet staticcheck deadcode

build:
	go build -o bin/$(BINARY) ./cmd/bot

test:
	go test ./...

ci: vet staticcheck deadcode

vet:
	go vet ./...

staticcheck:
	@if [ ! -x "$(STATICCHECK_BIN)" ]; then GOBIN=$(TOOLS_BIN) go install honnef.co/go/tools/cmd/staticcheck@latest; fi
	$(STATICCHECK_BIN) ./...

deadcode:
	@if [ ! -x "$(DEADCODE_BIN)" ]; then GOBIN=$(TOOLS_BIN) go install golang.org/x/tools/cmd/deadcode@latest; fi
	$(DEADCODE_BIN) ./...

run:
	go run ./cmd/bot -config internal/config/config.yaml
