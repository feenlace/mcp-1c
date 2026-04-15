package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/feenlace/mcp-1c/extension"
	"github.com/feenlace/mcp-1c/internal/config"
	"github.com/feenlace/mcp-1c/installer"
	"github.com/feenlace/mcp-1c/onec"
	"github.com/feenlace/mcp-1c/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is set at build time via ldflags:
//
//	go build -ldflags "-X main.version=0.4.2-beta" ./cmd/mcp-1c
var version = "dev"

const expectedExtensionVersion = "0.4.0"

func main() {
	log.SetOutput(os.Stderr)
	// MCP clients treat every stderr line as [error], so suppress INFO/WARN.
	// Only ERROR and above reach stderr, where [error] label is appropriate.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	showVersion := flag.Bool("version", false, "print version and exit")
	debug := flag.Bool("debug", false, "Enable verbose logging to file (~/.cache/mcp-1c/server.log)")
	baseURL := flag.String("base", "", "Base URL of 1C HTTP service")
	user := flag.String("user", "", "1C HTTP service user")
	password := flag.String("password", "", "1C HTTP service password")
	dumpDir := flag.String("dump", "", "Path to DumpConfigToFiles output (enables search_code)")
	cacheDir := flag.String("cache-dir", "", "Directory for index cache and logs (default: platform cache dir)")
	reindex := flag.Bool("reindex", false, "Force rebuild of search index cache")
	installDB := flag.String("install", "", "Install extension into 1C database at given path")
	serverMode := flag.Bool("server", false, `Treat --install value as server connection string (server\database)`)
	platformPath := flag.String("platform", "", "Path to 1C platform executable (auto-detected if omitted)")
	platformVersion := flag.String("platform-version", "", "1C platform version override (e.g. 8.3.13), auto-detected from path if omitted")
	dbUser := flag.String("db-user", "", "1C database user for DESIGNER (install mode)")
	dbPassword := flag.String("db-password", "", "1C database password for DESIGNER (install mode)")
	flag.Parse()

	if *cacheDir == "" {
		*cacheDir = os.Getenv("MCP_1C_CACHE_DIR")
	}

	// When --debug is set, redirect logs to a file at INFO level.
	// This avoids polluting stderr (which MCP clients show as errors)
	// while still capturing useful diagnostic output.
	if *debug {
		if f, err := openDebugLog("mcp-1c", *cacheDir); err == nil {
			log.SetOutput(f)
			slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})))
			defer f.Close()
		}
	}

	if *showVersion {
		fmt.Println("mcp-1c version " + version)
		os.Exit(0)
	}

	// Install mode.
	if *installDB != "" {
		fmt.Println("Installing MCP extension into 1C database...")
		if err := installer.Install(extension.Source, *installDB, *serverMode, *platformPath, *dbUser, *dbPassword, *platformVersion); err != nil {
			fmt.Fprintf(os.Stderr, "Installation error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Extension installed successfully.")
		return
	}

	// Load defaults and env var overrides.
	cfg := config.Load()

	// CLI flags take highest priority (override env vars).
	if *baseURL != "" {
		cfg.BaseURL = *baseURL
	}
	if *user != "" {
		cfg.User = *user
	}
	if *password != "" {
		cfg.Password = *password
	}

	client := onec.NewClient(cfg.BaseURL, cfg.User, cfg.Password)

	go checkExtensionVersion(client)

	var dumpIndex *dump.Index
	if *dumpDir != "" {
		var err error
		dumpIndex, err = dump.NewIndex(*dumpDir, *cacheDir, *reindex)
		if err != nil {
			fmt.Fprintf(os.Stderr, "loading dump from %s: %v\n", *dumpDir, err)
			os.Exit(1)
		}
		defer dumpIndex.Close()
		// Index builds in background. ModuleCount is available after Ready().
	}

	s := server.New(version, client, dumpIndex)

	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-1c error: %v\n", err)
		os.Exit(1)
	}
}

// openDebugLog creates (or truncates) a log file for debug output.
// The file is placed under the user cache directory:
//
//	macOS:   ~/Library/Caches/<name>/server.log
//	Linux:   ~/.cache/<name>/server.log
//	Windows: %LocalAppData%/<name>/server.log
func openDebugLog(name, cacheDir string) (*os.File, error) {
	var dir string
	if cacheDir != "" {
		dir = cacheDir
	} else {
		cacheBase, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(cacheBase, name)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return os.Create(filepath.Join(dir, "server.log"))
}

func checkExtensionVersion(client *onec.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var ver onec.VersionInfo
	if err := client.Get(ctx, "/version", &ver); err != nil {
		// Version endpoint may not exist in older extensions — skip silently.
		return
	}
	if ver.Version != expectedExtensionVersion {
		slog.Error("Extension version mismatch",
			"got", ver.Version, "expected", expectedExtensionVersion,
			"hint", `Update: mcp-1c --install "path\to\db"`)
	}
}
