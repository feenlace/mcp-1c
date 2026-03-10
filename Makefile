.PHONY: build test clean release mock

build:
	go build -o bin/mcp-1c ./cmd/mcp-1c

test:
	go test ./... -v -race

clean:
	rm -rf bin/ dist/

release:
	GOOS=windows GOARCH=amd64 go build -o dist/mcp-1c-windows-amd64.exe ./cmd/mcp-1c
	GOOS=windows GOARCH=arm64 go build -o dist/mcp-1c-windows-arm64.exe ./cmd/mcp-1c
	GOOS=linux GOARCH=amd64 go build -o dist/mcp-1c-linux-amd64 ./cmd/mcp-1c
	GOOS=linux GOARCH=arm64 go build -o dist/mcp-1c-linux-arm64 ./cmd/mcp-1c
	GOOS=darwin GOARCH=amd64 go build -o dist/mcp-1c-darwin-amd64 ./cmd/mcp-1c
	GOOS=darwin GOARCH=arm64 go build -o dist/mcp-1c-darwin-arm64 ./cmd/mcp-1c

mock:
	go run ./cmd/mock-1c
