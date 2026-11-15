.PHONY: build test clean release mock local-epf-rc

VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" -o bin/mcp-1c ./cmd/mcp-1c

test:
	go test ./... -v -race

clean:
	rm -rf bin/ dist/

release:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/mcp-1c-windows-amd64.exe ./cmd/mcp-1c
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/mcp-1c-windows-arm64.exe ./cmd/mcp-1c
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/mcp-1c-linux-amd64 ./cmd/mcp-1c
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/mcp-1c-linux-arm64 ./cmd/mcp-1c
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/mcp-1c-darwin-amd64 ./cmd/mcp-1c
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/mcp-1c-darwin-arm64 ./cmd/mcp-1c

mock:
	go run ./cmd/mock-1c

# Local evidence-bound candidate only; RC_OUTPUT must be a new directory.
local-epf-rc:
	python3 epf/tools/stage_local_rc.py --output "$(RC_OUTPUT)" $(if $(RC_PROOF),--proof "$(RC_PROOF)")
