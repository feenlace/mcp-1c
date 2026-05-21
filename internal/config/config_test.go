package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// Unset env vars to test defaults.
	t.Setenv("MCP_1C_BASE_URL", "")
	t.Setenv("MCP_1C_USER", "")
	t.Setenv("MCP_1C_PASSWORD", "")
	t.Setenv("MCP_1C_MAX_RESPONSE_SIZE", "")
	t.Setenv("MCP_1C_REQUEST_TIMEOUT", "")

	cfg := Load()

	if cfg.BaseURL != "http://localhost:8080/hs/mcp-1c" {
		t.Errorf("expected default base URL, got %s", cfg.BaseURL)
	}
	if cfg.User != "" {
		t.Errorf("expected empty user, got %s", cfg.User)
	}
	if cfg.Password != "" {
		t.Errorf("expected empty password, got %s", cfg.Password)
	}
	if cfg.MaxResponseSizeMiB != DefaultMaxResponseSizeMiB {
		t.Errorf("expected default max response size %d, got %d", DefaultMaxResponseSizeMiB, cfg.MaxResponseSizeMiB)
	}
	if cfg.RequestTimeout != DefaultRequestTimeout {
		t.Errorf("expected default request timeout %s, got %s", DefaultRequestTimeout, cfg.RequestTimeout)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	t.Setenv("MCP_1C_BASE_URL", "http://custom:9090/api")
	t.Setenv("MCP_1C_USER", "admin")
	t.Setenv("MCP_1C_PASSWORD", "secret")

	cfg := Load()

	if cfg.BaseURL != "http://custom:9090/api" {
		t.Errorf("expected overridden base URL, got %s", cfg.BaseURL)
	}
	if cfg.User != "admin" {
		t.Errorf("expected user admin, got %s", cfg.User)
	}
	if cfg.Password != "secret" {
		t.Errorf("expected password secret, got %s", cfg.Password)
	}
}

func TestLoadPartialOverride(t *testing.T) {
	t.Setenv("MCP_1C_BASE_URL", "")
	t.Setenv("MCP_1C_USER", "operator")
	t.Setenv("MCP_1C_PASSWORD", "")

	cfg := Load()

	if cfg.BaseURL != "http://localhost:8080/hs/mcp-1c" {
		t.Errorf("expected default base URL, got %s", cfg.BaseURL)
	}
	if cfg.User != "operator" {
		t.Errorf("expected user operator, got %s", cfg.User)
	}
	if cfg.Password != "" {
		t.Errorf("expected empty password, got %s", cfg.Password)
	}
}

func TestLoadMaxResponseSize(t *testing.T) {
	t.Run("env override", func(t *testing.T) {
		t.Setenv("MCP_1C_MAX_RESPONSE_SIZE", "256")
		cfg := Load()
		if cfg.MaxResponseSizeMiB != 256 {
			t.Errorf("expected 256 MiB, got %d", cfg.MaxResponseSizeMiB)
		}
	})

	t.Run("invalid value falls back to default", func(t *testing.T) {
		t.Setenv("MCP_1C_MAX_RESPONSE_SIZE", "not-a-number")
		cfg := Load()
		if cfg.MaxResponseSizeMiB != DefaultMaxResponseSizeMiB {
			t.Errorf("expected default %d on invalid value, got %d", DefaultMaxResponseSizeMiB, cfg.MaxResponseSizeMiB)
		}
	})

	t.Run("non-positive value falls back to default", func(t *testing.T) {
		t.Setenv("MCP_1C_MAX_RESPONSE_SIZE", "0")
		cfg := Load()
		if cfg.MaxResponseSizeMiB != DefaultMaxResponseSizeMiB {
			t.Errorf("expected default %d on zero value, got %d", DefaultMaxResponseSizeMiB, cfg.MaxResponseSizeMiB)
		}
	})
}

func TestLoadRequestTimeout(t *testing.T) {
	t.Run("env override", func(t *testing.T) {
		t.Setenv("MCP_1C_REQUEST_TIMEOUT", "600")
		cfg := Load()
		if cfg.RequestTimeout != 600*time.Second {
			t.Errorf("expected 600s, got %s", cfg.RequestTimeout)
		}
	})

	t.Run("invalid value falls back to default", func(t *testing.T) {
		t.Setenv("MCP_1C_REQUEST_TIMEOUT", "abc")
		cfg := Load()
		if cfg.RequestTimeout != DefaultRequestTimeout {
			t.Errorf("expected default %s on invalid value, got %s", DefaultRequestTimeout, cfg.RequestTimeout)
		}
	})

	t.Run("non-positive value falls back to default", func(t *testing.T) {
		t.Setenv("MCP_1C_REQUEST_TIMEOUT", "-5")
		cfg := Load()
		if cfg.RequestTimeout != DefaultRequestTimeout {
			t.Errorf("expected default %s on negative value, got %s", DefaultRequestTimeout, cfg.RequestTimeout)
		}
	})
}
