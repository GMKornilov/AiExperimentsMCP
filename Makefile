.PHONY: build test vet demo-mcp demo-agent
build:
	go build ./...
test:
	go test ./...
vet:
	go vet ./...
demo-mcp:
	go run ./cmd/demo-mcp
demo-agent:
	go run ./cmd/agent --config config.demo.yaml
