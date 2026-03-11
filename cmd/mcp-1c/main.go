package main

import (
	"context"
	"flag"
	"fmt"
	"os"
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
	baseURL := flag.String("base", "", "Base URL of 1C HTTP service")
	user := flag.String("user", "", "1C HTTP service user")
	password := flag.String("password", "", "1C HTTP service password")
	dumpDir := flag.String("dump", "", "Path to DumpConfigToFiles output (enables search_code tool)")
	installDB := flag.String("install", "", "Install extension into 1C database at given path")
	serverMode := flag.Bool("server", false, `Treat --install value as server connection string (server\database)`)
	platformPath := flag.String("platform", "", "Path to 1C platform executable (auto-detected if omitted)")
	dbUser := flag.String("db-user", "", "1C database user for DESIGNER (install mode)")
	dbPassword := flag.String("db-password", "", "1C database password for DESIGNER (install mode)")
	flag.Parse()

	// Install mode.
	if *installDB != "" {
		fmt.Println("Installing MCP extension into 1C database...")
		if err := installer.Install(extension.Source, *installDB, *serverMode, *platformPath, *dbUser, *dbPassword); err != nil {
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

	checkExtensionVersion(client)

	var dumpIndex *dump.Index
	if *dumpDir != "" {
		var err error
		dumpIndex, err = dump.NewIndex(*dumpDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "loading dump from %s: %v\n", *dumpDir, err)
			os.Exit(1)
		}
		defer dumpIndex.Close()
		fmt.Fprintf(os.Stderr, "Indexed %d BSL modules from dump\n", dumpIndex.ModuleCount())
	}

	s := server.New(version, client, dumpIndex)

	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-1c error: %v\n", err)
		os.Exit(1)
	}
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
		fmt.Fprintf(os.Stderr, "WARNING: Extension version %s, expected %s. Update: mcp-1c --install \"path\\to\\db\"\n",
			ver.Version, expectedExtensionVersion)
	}
}
