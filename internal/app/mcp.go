package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	return t.base.RoundTrip(r)
}
func (s *Service) connectMCP(ctx context.Context, c MCPServerConfig) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "scheduled-agent", Version: "1.0.0"}, nil)
	client.AddSendingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			started := time.Now()
			callID := s.nextCallID()
			requestLog, requestTruncated := s.logPayload(req.GetParams())
			tool := ""
			if params, ok := req.GetParams().(*mcp.CallToolParams); ok {
				tool = params.Name
			}
			base := []any{"call_id", callID, "server", c.Name, "method", method, "tool", tool}
			s.logCall(ctx, "mcp_call", append(append([]any{}, base...), "status", "started", "request", requestLog, "request_truncated", requestTruncated)...)
			result, err := next(ctx, method, req)
			responseLog, responseTruncated := s.logPayload(result)
			status, errorText := "ok", ""
			if err != nil {
				status, errorText = "error", s.redactLogString(err.Error())
			}
			if toolResult, ok := result.(*mcp.CallToolResult); ok && toolResult.IsError {
				status, errorText = "error", "MCP tool returned an error"
			}
			s.logCall(ctx, "mcp_call", append(base,
				"duration_ms", time.Since(started).Milliseconds(), "status", status, "request", requestLog,
				"request_truncated", requestTruncated, "response", responseLog, "response_truncated", responseTruncated, "error", errorText)...)
			return result, err
		}
	})
	hc := &http.Client{Transport: headerTransport{base: http.DefaultTransport, headers: c.Headers}}
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: c.URL, HTTPClient: hc, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return nil, fmt.Errorf("MCP server %s unavailable: %w", c.Name, err)
	}
	return session, nil
}
func toolAPIName(server, name string) string {
	h := sha256.Sum256([]byte(server + "\x00" + name))
	prefix := "mcp_" + hex.EncodeToString(h[:10])
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, name)
	if len(clean) > 38 {
		clean = clean[:38]
	}
	return prefix + "_" + clean
}
func (s *Service) discover(ctx context.Context) ([]Tool, []ServerStatus, error) {
	out := []Tool{}
	statuses := []ServerStatus{}
	seen := map[string]bool{}
	for _, cfg := range s.cfg.MCPServers {
		session, err := s.connectMCP(ctx, cfg)
		if err != nil {
			statuses = append(statuses, ServerStatus{Name: cfg.Name, Status: "error", Error: err.Error()})
			continue
		}
		for tool, err := range session.Tools(ctx, nil) {
			if err != nil {
				statuses = append(statuses, ServerStatus{Name: cfg.Name, Status: "error", Error: fmt.Sprintf("list tools: %v", err)})
				break
			}
			apiName := toolAPIName(cfg.Name, tool.Name)
			if seen[apiName] {
				statuses = append(statuses, ServerStatus{Name: cfg.Name, Status: "error", Error: "tool namespace collision"})
				break
			}
			seen[apiName] = true
			out = append(out, Tool{Name: tool.Name, Server: cfg.Name, Description: tool.Description, InputSchema: tool.InputSchema, apiName: apiName})
		}
		session.Close()
		found := false
		for _, st := range statuses {
			if st.Name == cfg.Name {
				found = true
				break
			}
		}
		if !found {
			statuses = append(statuses, ServerStatus{Name: cfg.Name, Status: "ok"})
		}
	}
	for _, st := range statuses {
		if st.Status != "ok" {
			return out, statuses, fmt.Errorf("MCP discovery failed: %s: %s", st.Name, st.Error)
		}
	}
	return out, statuses, nil
}
func (s *Service) callMCP(ctx context.Context, server, tool string, args json.RawMessage) (string, error) {
	var cfg *MCPServerConfig
	for i := range s.cfg.MCPServers {
		if s.cfg.MCPServers[i].Name == server {
			cfg = &s.cfg.MCPServers[i]
			break
		}
	}
	if cfg == nil {
		return "", fmt.Errorf("unknown MCP server %s", server)
	}
	session, err := s.connectMCP(ctx, *cfg)
	if err != nil {
		return "", err
	}
	defer session.Close()
	var v map[string]any
	if err = json.Unmarshal(args, &v); err != nil {
		return "", fmt.Errorf("invalid tool arguments: %w", err)
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: v})
	if err != nil {
		return "", fmt.Errorf("MCP tool call failed: %w", err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	if res.IsError {
		return string(b), fmt.Errorf("MCP tool returned an error")
	}
	return string(b), nil
}
