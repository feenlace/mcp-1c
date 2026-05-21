package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds the MCP server configuration.
type Config struct {
	BaseURL  string
	User     string
	Password string

	// MaxResponseSizeMiB — лимит размера ответа 1С в мебибайтах (MiB).
	// Ответ крупнее отбрасывается с понятной ошибкой вместо OOM или
	// невнятного «unexpected EOF».
	MaxResponseSizeMiB int

	// RequestTimeout — таймаут HTTP-запроса к 1С. Запас нужен для передачи
	// крупных ответов /extensions (сотни мегабайт).
	RequestTimeout time.Duration
}

// DefaultMaxResponseSizeMiB — лимит размера ответа 1С по умолчанию (MiB).
const DefaultMaxResponseSizeMiB = 128

// DefaultRequestTimeout — таймаут HTTP-запроса к 1С по умолчанию.
const DefaultRequestTimeout = 5 * time.Minute

// Load reads configuration from environment variables.
// CLI flags should be parsed in main.go before calling Load.
// Environment variables override any values already set in the Config.
func Load() *Config {
	cfg := &Config{
		BaseURL:            "http://localhost:8080/hs/mcp-1c",
		MaxResponseSizeMiB: DefaultMaxResponseSizeMiB,
		RequestTimeout:     DefaultRequestTimeout,
	}

	if v := os.Getenv("MCP_1C_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := os.Getenv("MCP_1C_USER"); v != "" {
		cfg.User = v
	}
	if v := os.Getenv("MCP_1C_PASSWORD"); v != "" {
		cfg.Password = v
	}
	// MCP_1C_MAX_RESPONSE_SIZE задаётся в мебибайтах (MiB). Некорректные или
	// неположительные значения игнорируются — остаётся значение по умолчанию.
	if v := os.Getenv("MCP_1C_MAX_RESPONSE_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxResponseSizeMiB = n
		}
	}
	// MCP_1C_REQUEST_TIMEOUT задаётся в секундах. Некорректные или
	// неположительные значения игнорируются — остаётся значение по умолчанию.
	if v := os.Getenv("MCP_1C_REQUEST_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.RequestTimeout = time.Duration(n) * time.Second
		}
	}

	return cfg
}
