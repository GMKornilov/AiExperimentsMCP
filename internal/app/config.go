package app

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type MCPServerConfig struct {
	Name    string            `yaml:"name" json:"name"`
	URL     string            `yaml:"url" json:"url"`
	Headers map[string]string `yaml:"headers" json:"-"`
}
type LLMConfig struct {
	Provider      string   `yaml:"provider"`
	Service       string   `yaml:"service"`
	Model         string   `yaml:"model"`
	BaseURL       string   `yaml:"base_url"`
	APIKey        string   `yaml:"api_key"`
	Temperature   *float64 `yaml:"temperature"`
	MaxIterations int      `yaml:"max_iterations"`
}
type Config struct {
	Listen      string            `yaml:"listen"`
	Database    string            `yaml:"database"`
	Timezone    string            `yaml:"timezone"`
	TaskTimeout string            `yaml:"task_timeout"`
	LLM         LLMConfig         `yaml:"llm"`
	MCPServers  []MCPServerConfig `yaml:"mcp_servers"`
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func LoadConfig(path, envFile string) (Config, error) {
	if err := loadEnvFile(envFile); err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("parse config: %w", err)
	}
	for _, field := range []*string{&c.Listen, &c.Database, &c.Timezone, &c.TaskTimeout, &c.LLM.Provider, &c.LLM.Service, &c.LLM.Model, &c.LLM.BaseURL, &c.LLM.APIKey} {
		*field, err = expandEnv(*field)
		if err != nil {
			return c, err
		}
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if c.Database == "" {
		c.Database = "data/agent.db"
	}
	if c.Timezone == "" {
		c.Timezone = "UTC"
	}
	if c.TaskTimeout == "" {
		c.TaskTimeout = "5m"
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return c, fmt.Errorf("timezone: %w", err)
	}
	if timeout, err := time.ParseDuration(c.TaskTimeout); err != nil || timeout <= 0 {
		return c, fmt.Errorf("task_timeout must be positive duration")
	}
	if c.LLM.Provider == "" {
		c.LLM.Provider = "openai-compatible"
	}
	if c.LLM.Provider != "demo" && c.LLM.Provider != "openai-compatible" {
		return c, fmt.Errorf("llm.provider must be demo or openai-compatible")
	}
	if c.LLM.MaxIterations == 0 {
		c.LLM.MaxIterations = 8
	}
	if c.LLM.MaxIterations < 1 || c.LLM.MaxIterations > 32 {
		return c, fmt.Errorf("llm.max_iterations must be 1..32")
	}
	if c.LLM.Temperature == nil {
		v := 1.0
		c.LLM.Temperature = &v
	}
	if math.IsNaN(*c.LLM.Temperature) || math.IsInf(*c.LLM.Temperature, 0) || *c.LLM.Temperature < 0 || *c.LLM.Temperature > 2 {
		return c, fmt.Errorf("llm.temperature must be 0..2")
	}
	if c.LLM.Provider != "demo" {
		if c.LLM.Model == "" {
			return c, fmt.Errorf("llm.model is required")
		}
		if c.LLM.BaseURL == "" {
			return c, fmt.Errorf("llm.base_url is required")
		}
		if c.LLM.APIKey == "" {
			return c, fmt.Errorf("llm.api_key is required")
		}
		u, e := url.Parse(c.LLM.BaseURL)
		if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, fmt.Errorf("llm.base_url must be http(s), without credentials, query or fragment")
		}
	}
	if c.LLM.Service == "" {
		c.LLM.Service = c.LLM.Provider
	}
	seen := map[string]bool{}
	for i := range c.MCPServers {
		s := &c.MCPServers[i]
		if s.Name, err = expandEnv(s.Name); err != nil {
			return c, err
		}
		if s.Name == "" || seen[s.Name] {
			return c, fmt.Errorf("mcp server name empty or duplicate")
		}
		seen[s.Name] = true
		if s.URL, err = expandEnv(s.URL); err != nil {
			return c, fmt.Errorf("mcp server %s url: %w", s.Name, err)
		}
		u, e := url.Parse(s.URL)
		if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, fmt.Errorf("mcp server %s: URL must be http(s), without credentials, query or fragment; use headers", s.Name)
		}
		for k, v := range s.Headers {
			if s.Headers[k], err = expandEnv(v); err != nil {
				return c, fmt.Errorf("mcp server %s header %s: %w", s.Name, k, err)
			}
		}
	}
	return c, nil
}
func expandEnv(s string) (string, error) {
	var missing string
	out := envPattern.ReplaceAllStringFunc(s, func(m string) string {
		k := m[2 : len(m)-1]
		v, ok := os.LookupEnv(k)
		if !ok {
			missing = k
		}
		return v
	})
	if missing != "" {
		return "", fmt.Errorf("environment variable %s is not set", missing)
	}
	return out, nil
}
func loadEnvFile(path string) error {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open env file: %w", err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("invalid env file line")
		}
		k = strings.TrimSpace(k)
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(k) {
			return fmt.Errorf("invalid env variable name")
		}
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "\"") {
			unq, e := strconv.Unquote(v)
			if e != nil {
				return fmt.Errorf("invalid env file quoted value")
			}
			v = unq
		} else if strings.HasPrefix(v, "'") {
			if len(v) < 2 || v[len(v)-1] != '\'' {
				return fmt.Errorf("invalid env file quoted value")
			}
			v = v[1 : len(v)-1]
		}
		if _, ok := os.LookupEnv(k); !ok {
			if err := os.Setenv(k, v); err != nil {
				return fmt.Errorf("set env variable %s: %w", k, err)
			}
		}
	}
	return s.Err()
}
