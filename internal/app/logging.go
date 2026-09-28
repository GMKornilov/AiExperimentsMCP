package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

const logPayloadLimit = 8 << 10

type traceKey struct{}
type callTrace struct {
	taskID string
	stage  string
}

func withTrace(ctx context.Context, taskID, stage string) context.Context {
	return context.WithValue(ctx, traceKey{}, callTrace{taskID: taskID, stage: stage})
}

func traceAttrs(ctx context.Context) []any {
	trace, _ := ctx.Value(traceKey{}).(callTrace)
	return []any{"task_id", trace.taskID, "stage", trace.stage}
}

func (s *Service) nextCallID() string {
	return fmt.Sprintf("call-%d", s.callSeq.Add(1))
}

var bearerPattern = regexp.MustCompile(`(?i)\b(?:Bearer|Basic)\s+[^\s"'<>]+`)
var urlSecretPattern = regexp.MustCompile(`(?i)([?&](?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|password|secret)=)[^&#\s]+`)
var fieldSecretPattern = regexp.MustCompile(`(?i)(["']?(?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|password|secret|client[_-]?secret)["']?\s*[:=]\s*["']?)[^"'\s,}]+`)

func (s *Service) redactLogString(input string) string {
	out := input
	secrets := []string{s.cfg.LLM.APIKey, s.cfg.LLM.BaseURL}
	for _, server := range s.cfg.MCPServers {
		secrets = append(secrets, server.URL)
		for key, value := range server.Headers {
			secrets = append(secrets, value)
			if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Proxy-Authorization") {
				if _, credential, ok := strings.Cut(value, " "); ok {
					secrets = append(secrets, strings.TrimSpace(credential))
				}
			}
		}
	}
	for _, secret := range secrets {
		if secret != "" {
			variants := []string{secret}
			encoded := secret
			for i := 0; i < 2; i++ {
				b, _ := json.Marshal(encoded)
				encoded = string(b[1 : len(b)-1])
				variants = append(variants, encoded)
			}
			for i := len(variants) - 1; i >= 0; i-- {
				out = strings.ReplaceAll(out, variants[i], "[REDACTED]")
			}
		}
	}
	// Error bodies can contain JSON fragments whose quotes are escaped more than once.
	for i := 0; i < 3; i++ {
		out = strings.ReplaceAll(out, `\"`, `"`)
	}
	for _, secret := range secrets {
		if secret != "" {
			out = strings.ReplaceAll(out, secret, "[REDACTED]")
		}
	}
	out = bearerPattern.ReplaceAllString(out, "Bearer [REDACTED]")
	out = urlSecretPattern.ReplaceAllString(out, "${1}[REDACTED]")
	return fieldSecretPattern.ReplaceAllString(out, "${1}[REDACTED]")
}

func (s *Service) safeDiagnostic(input string) string {
	var value any
	if json.Unmarshal([]byte(input), &value) == nil {
		if b, err := json.Marshal(s.sanitizeLogValue(value)); err == nil {
			input = string(b)
		}
	}
	input = s.redactLogString(input)
	if len(input) > 1024 {
		return input[:1024] + "…[truncated]"
	}
	return input
}

func sensitiveKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
	switch key {
	case "apikey", "authorization", "proxyauthorization", "accesstoken", "refreshtoken", "token", "password", "passwd", "secret", "clientsecret", "privatekey", "credential", "credentials":
		return true
	}
	return false
}

func (s *Service) sanitizeLogValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if sensitiveKey(key) {
				out[key] = "[REDACTED]"
			} else {
				out[key] = s.sanitizeLogValue(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = s.sanitizeLogValue(item)
		}
		return out
	case string:
		// Tool arguments and content may themselves be JSON encoded as a string.
		var nested any
		if json.Unmarshal([]byte(v), &nested) == nil {
			switch nested.(type) {
			case map[string]any, []any:
				if b, err := json.Marshal(s.sanitizeLogValue(nested)); err == nil {
					return s.redactLogString(string(b))
				}
			}
		}
		return s.redactLogString(v)
	default:
		return v
	}
}

func (s *Service) logPayload(value any) (string, bool) {
	b, err := json.Marshal(value)
	if err != nil {
		return "[unavailable]", false
	}
	var decoded any
	if err := json.Unmarshal(b, &decoded); err != nil {
		return "[unavailable]", false
	}
	b, err = json.Marshal(s.sanitizeLogValue(decoded))
	if err != nil {
		return "[unavailable]", false
	}
	if len(b) > logPayloadLimit {
		return string(b[:logPayloadLimit]), true
	}
	return string(b), false
}

func (s *Service) logCall(ctx context.Context, event string, fields ...any) {
	if s.logger == nil {
		return
	}
	attrs := append(traceAttrs(ctx), fields...)
	s.logger.Log(ctx, slog.LevelInfo, event, attrs...)
}
