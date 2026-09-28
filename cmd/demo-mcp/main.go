package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type EchoInput struct {
	Text string `json:"text" jsonschema:"text to repeat"`
}
type EchoOutput struct {
	Text string `json:"text"`
}
type TimeInput struct{}
type TimeOutput struct {
	Time string `json:"time"`
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8081", "listen address")
	flag.Parse()
	server := mcp.NewServer(&mcp.Implementation{Name: "demo-mcp", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Return the supplied text"}, func(_ context.Context, _ *mcp.CallToolRequest, in EchoInput) (*mcp.CallToolResult, EchoOutput, error) {
		return nil, EchoOutput{Text: in.Text}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "get_time", Description: "Return current UTC time"}, func(_ context.Context, _ *mcp.CallToolRequest, _ TimeInput) (*mcp.CallToolResult, TimeOutput, error) {
		return nil, TimeOutput{Time: time.Now().UTC().Format(time.RFC3339)}, nil
	})
	log.Printf("demo MCP listening on %s", *listen)
	log.Fatal(http.ListenAndServe(*listen, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)))
}
