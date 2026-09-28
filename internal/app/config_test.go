package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aichallenge_scheduled/internal/app"
)

func writeTestConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigTemperatureDefaultAndExplicitZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want float64
	}{
		{"default", "llm:\n  provider: demo\n", 1},
		{"explicit zero", "llm:\n  provider: demo\n  temperature: 0\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := app.LoadConfig(writeTestConfig(t, tc.yaml), "")
			if err != nil {
				t.Fatal(err)
			}
			if got.LLM.Temperature == nil || *got.LLM.Temperature != tc.want {
				t.Fatalf("temperature = %v, want %v", got.LLM.Temperature, tc.want)
			}
		})
	}
}

func TestLoadConfigEnvExpansionAndPriority(t *testing.T) {
	const name = "AICHALLENGE_TEST_SECRET_ENV_PRIORITY"
	t.Setenv(name, "process:value # with\nnew line")
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte(name+"=file-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := writeTestConfig(t, "llm:\n  provider: openai-compatible\n  model: test\n  base_url: http://127.0.0.1:12345/v1\n  api_key: ${"+name+"}\n")
	got, err := app.LoadConfig(path, envFile)
	if err != nil {
		t.Fatal(err)
	}
	if got.LLM.APIKey != "process:value # with\nnew line" {
		t.Fatalf("api key substitution changed value: %q", got.LLM.APIKey)
	}
	if got.LLM.Model != "test" {
		t.Fatalf("environment value injected YAML: model = %q", got.LLM.Model)
	}
}

func TestLoadConfigMissingEnvironmentVariable(t *testing.T) {
	const name = "AICHALLENGE_TEST_CERTAINLY_MISSING_88A7"
	if _, ok := os.LookupEnv(name); ok {
		t.Skip("reserved test environment variable is set")
	}
	path := writeTestConfig(t, "llm:\n  provider: openai-compatible\n  model: test\n  base_url: http://127.0.0.1:12345/v1\n  api_key: ${"+name+"}\n")
	_, err := app.LoadConfig(path, "")
	if err == nil || !strings.Contains(err.Error(), name) {
		t.Fatalf("expected missing %s error, got %v", name, err)
	}
}

func TestLoadConfigReadsEnvFileWhenProcessVariableAbsent(t *testing.T) {
	const name = "AICHALLENGE_TEST_SECRET_ONLY_IN_FILE_974D"
	if _, ok := os.LookupEnv(name); ok {
		t.Skip("reserved test environment variable is set")
	}
	t.Cleanup(func() { _ = os.Unsetenv(name) })
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte(name+"=from-file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := writeTestConfig(t, "llm:\n  provider: openai-compatible\n  model: test\n  base_url: http://127.0.0.1:12345/v1\n  api_key: ${"+name+"}\n")
	got, err := app.LoadConfig(path, envFile)
	if err != nil {
		t.Fatal(err)
	}
	if got.LLM.APIKey != "from-file" {
		t.Fatalf("api key = %q", got.LLM.APIKey)
	}
}
