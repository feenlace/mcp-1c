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
	"github.com/feenlace/mcp-1c/installer"
	"github.com/feenlace/mcp-1c/internal/config"
	"github.com/feenlace/mcp-1c/onec"
	"github.com/feenlace/mcp-1c/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/term"
)

// version is set at build time via ldflags:
//
//	go build -ldflags "-X main.version=0.4.2-beta" ./cmd/mcp-1c
var version = "dev"

const expectedExtensionVersion = "0.4.6"

func main() {
	log.SetOutput(os.Stderr)
	// MCP clients treat every stderr line as [error], so suppress INFO/WARN.
	// Only ERROR and above reach stderr, where [error] label is appropriate.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	showVersion := flag.Bool("version", false, "print version and exit")
	debug := flag.Bool("debug", false, "Enable verbose logging to file (~/.cache/mcp-1c/server.log). In terminal mode also suppresses the stderr progress indicator.")
	baseURL := flag.String("base", "", "Base URL of 1C HTTP service")
	user := flag.String("user", "", "1C HTTP service user")
	password := flag.String("password", "", "1C HTTP service password")
	dumpDir := flag.String("dump", "", "Path to DumpConfigToFiles output (enables search_code)")
	cacheDir := flag.String("cache-dir", "", "Directory for index cache and logs (default: platform cache dir)")
	reindex := flag.Bool("reindex", false, "Force rebuild of search index cache")
	buildIndex := flag.Bool("build-index", false, "Build (or refresh) the search index cache for --dump and exit. Pre-warms the on-disk cache so a later start opens it instantly instead of doing an in-memory cold build. Requires --dump and a writable cache dir (--cache-dir or MCP_1C_CACHE_DIR).")
	installDB := flag.String("install", "", "Install extension into 1C database at given path")
	serverMode := flag.Bool("server", false, `Treat --install value as server connection string (server\database)`)
	platformPath := flag.String("platform", "", "Path to 1C platform executable (auto-detected if omitted)")
	platformVersion := flag.String("platform-version", "", "1C platform version override (e.g. 8.3.13), auto-detected from path if omitted")
	dbUser := flag.String("db-user", "", "1C database user for DESIGNER (install mode)")
	dbPassword := flag.String("db-password", "", "1C database password for DESIGNER (install mode)")
	quiet := flag.Bool("quiet", false, "Suppress all stderr output even when running in a terminal. Takes precedence over --verbose. Also activated by env MCP_1C_NO_TTY=1.")
	verbose := flag.Bool("verbose", false, "Force verbose stderr output even when stdin is a pipe (useful for MCP client debugging). Overrides auto-detect and is itself overridden by --quiet.")
	// Sentinel 0 => "flag not passed", so the MCP_1C_MAX_RESPONSE_SIZE env var
	// (read in config.Load) keeps effect; a passed flag overrides it.
	maxResponseSize := flag.Int("max-response-size", 0, "Maximum size of a 1C HTTP response, in mebibytes (MiB). A larger response is rejected with a clear error instead of a cryptic decode failure. Default: 128.")
	requestTimeout := flag.Int("request-timeout", 0, "Timeout for an HTTP request to 1C, in seconds. Raise it if fetching a very large response (e.g. extensions of a big database) times out. Default: 300.")
	flag.Parse()

	if *cacheDir == "" {
		*cacheDir = os.Getenv("MCP_1C_CACHE_DIR")
	}

	// Env var override: MCP_1C_NO_TTY=1 forces non-TTY (quiet) mode.
	// More convenient than --quiet in Docker/systemd environments where
	// CLI arguments are less flexible than environment variables.
	if os.Getenv("MCP_1C_NO_TTY") == "1" {
		*quiet = true
	}

	// Effective TTY mode: true => print info logs and progress to stderr (as in v1.6.0),
	// false => suppress stderr (as in v1.6.1). Manual overrides take precedence.
	stdinIsTTY := term.IsTerminal(int(os.Stdin.Fd()))
	effectiveTTY := stdinIsTTY
	if *verbose {
		effectiveTTY = true
	}
	if *quiet {
		effectiveTTY = false
	}

	// When --debug is set, redirect logs to a file at INFO level.
	// This avoids polluting stderr (which MCP clients show as errors)
	// while still capturing useful diagnostic output.
	if *debug {
		if t, err := openDebugLog("mcp-1c", *cacheDir); err == nil {
			log.SetOutput(t.file)
			slog.SetDefault(slog.New(slog.NewTextHandler(t.file, &slog.HandlerOptions{Level: slog.LevelInfo})))
			defer t.file.Close()
			reportLogFallback(t)
		}
	}

	// Record the binary version for the dump package so it can be written into the
	// cache folder's dump.json (the dump package cannot import main).
	dump.BuildVersion = version

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

	// Build-index mode: offline pre-warm of the search cache, then exit. Mirrors
	// the install/version command modes and runs before the MCP stderr redirect,
	// so build progress is visible on a terminal launch.
	if *buildIndex {
		if *dumpDir == "" {
			fmt.Fprintln(os.Stderr, "--build-index requires --dump")
			os.Exit(2)
		}
		dump.SetShowProgress(effectiveTTY && !*debug)
		fmt.Printf("Building search index for %s ...\n", *dumpDir)
		start := time.Now()
		if err := dump.BuildCache(*dumpDir, *cacheDir, *reindex); err != nil {
			fmt.Fprintf(os.Stderr, "build-index failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Index cache built in %.1fs.\n", time.Since(start).Seconds())
		return
	}

	// Three modes of stderr handling (outside of --debug):
	//   1. effectiveTTY=true  => show info logs and progress in terminal (v1.6.0 behaviour)
	//   2. effectiveTTY=false => redirect stderr to a file to protect strict MCP
	//      clients (Kilo Code 7.x, Issue #14) from any third-party stderr writes
	//      (v1.6.1 behaviour)
	// --debug overrides both and writes everything to server.log at INFO level.
	if !*debug {
		if effectiveTTY {
			// Terminal launch: show info-level logs and progress to the user.
			log.SetOutput(os.Stderr)
			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
		} else {
			// Pipe launch (MCP client): redirect stderr to a file so third-party
			// libraries (bleve, scorch) cannot trigger a restart loop.
			if t, err := openStderrLog("mcp-1c", *cacheDir); err == nil {
				os.Stderr = t.file
				log.SetOutput(t.file)
				slog.SetDefault(slog.New(slog.NewTextHandler(t.file, &slog.HandlerOptions{Level: slog.LevelError})))
				reportLogFallback(t)
			} else {
				// Fallback: if we cannot create the stderr log file (cacheDir
				// missing, disk full, permission denied), we MUST still redirect
				// stderr away from the MCP client pipe. Otherwise third-party
				// library writes can trigger the restart loop we are trying
				// to prevent. Redirect to os.DevNull instead.
				if devnull, errNull := os.OpenFile(os.DevNull, os.O_WRONLY, 0); errNull == nil {
					os.Stderr = devnull
					log.SetOutput(devnull)
					slog.SetDefault(slog.New(slog.NewTextHandler(devnull, &slog.HandlerOptions{Level: slog.LevelError})))
				} else {
					// Last resort: keep the real stderr but at least log the
					// failure so it shows up in any debug log.
					slog.Warn("cannot redirect stderr",
						"log_err", err, "devnull_err", errNull)
				}
			}
		}
	}

	// Tell dump package whether to print progress ticker and info lines.
	dump.SetShowProgress(effectiveTTY && !*debug)

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
	if *maxResponseSize > 0 {
		cfg.MaxResponseSizeMiB = *maxResponseSize
	}
	if *requestTimeout > 0 {
		cfg.RequestTimeout = time.Duration(*requestTimeout) * time.Second
	}

	client := onec.NewClient(cfg.BaseURL, cfg.User, cfg.Password,
		onec.WithMaxResponseSize(cfg.MaxResponseSizeMiB),
		onec.WithRequestTimeout(cfg.RequestTimeout),
	)

	go checkExtensionVersion(client)

	// serveBuildCtx bounds any background generation build kicked off by
	// openServeIndexLocal so a build still in flight when serving ends cannot wedge
	// process exit. It is cancelled explicitly right after s.Run returns (and via the
	// defer as a backstop) so the deferred dumpIndex.Close() does not block on it.
	serveBuildCtx, serveBuildCancel := context.WithCancel(context.Background())
	defer serveBuildCancel()

	var dumpIndex *dump.Index
	if *dumpDir != "" {
		var err error
		dumpIndex, err = openServeIndexLocal(serveBuildCtx, *dumpDir, *cacheDir, *reindex)
		if err != nil {
			fmt.Fprintf(os.Stderr, "loading dump from %s: %v\n", *dumpDir, err)
			os.Exit(1)
		}
		defer dumpIndex.Close()
		// Index prepares in the background. ModuleCount is available after Ready().
	}

	s := server.New(version, client, dumpIndex)

	runErr := s.Run(context.Background(), &mcp.StdioTransport{})
	// Serving has ended: stop any background build now so it cannot wedge shutdown
	// (the deferred dumpIndex.Close() waits on the index's Done()).
	serveBuildCancel()
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "mcp-1c error: %v\n", runErr)
		os.Exit(1)
	}
}

// logRollAtBytes caps a log file's carried-over history. Below it the file is
// APPENDED to (see openLogFile); at or above it the next process to open the file
// starts it over, so the disk cost stays bounded without a rotation scheme and
// without the race a rename between co-located processes would add.
const logRollAtBytes = 8 << 20 // 8 MiB

// logTarget is the log file a run actually got, plus what it asked for. requested
// and cause are set ONLY when the requested directory could not be used, so a
// non-nil cause is the signal to report the substitution — into the file itself,
// which is the only place a stdio-mode process can report anything.
type logTarget struct {
	file      *os.File
	path      string
	requested string
	cause     error
}

// openDebugLog opens the log file for debug output. The file is placed under the
// user cache directory:
//
//	macOS:   ~/Library/Caches/<name>/server.log
//	Linux:   ~/.cache/<name>/server.log
//	Windows: %LocalAppData%/<name>/server.log
func openDebugLog(name, cacheDir string) (*logTarget, error) {
	return openLogFile(name, cacheDir, "server.log")
}

// openStderrLog opens the file that captures stderr output in MCP stdio mode. The
// redirect protects strict MCP clients (Kilo Code 7.x) from crashing on stderr
// writes produced by third-party libraries.
func openStderrLog(name, cacheDir string) (*logTarget, error) {
	return openLogFile(name, cacheDir, "stderr.log")
}

// openLogFile opens a log file for this run, preferring the cache directory and
// falling back to somewhere writable when it is not.
//
// THE FALLBACK IS THE POINT, and it was measured. This file used to be created
// inside cacheDir and nowhere else, and main's handler for a failure to create it
// is to send slog to os.DevNull. A cache directory this process may read but not
// write therefore destroyed the only channel that could report it. As measured at
// the time, when such a cache still made the server refuse every search: stderr.log
// stayed 0 bytes across a warm-up and three runs, while the same refusal on a cache
// whose root was writable produced 662 bytes and one ERROR line. The refusal is
// gone — an unwritable cache is served now, and reported — but the log defect it
// exposed was never about the refusal: the level was never wrong, the destination
// was, and an unwritable cache is exactly when there is something to say. So a
// directory that cannot take the log is not fatal here: the
// platform cache dir is tried next (it is the default precisely because it is
// writable), then the OS temp dir, and only a machine where none of the three works
// reaches main's DevNull branch.
//
// IT APPENDS. It used to end in os.Create, which truncates, so in the shared arena
// this whole change set exists to protect every process that started wiped the log
// of the ones before it — measured at 36 processes, one surviving record. Appending
// is what makes a multi-process arena auditable at all. Growth is bounded by
// logRollAtBytes rather than by throwing the history away on every start.
func openLogFile(name, cacheDir, filename string) (*logTarget, error) {
	requested, err := preferredLogDir(name, cacheDir)
	if err != nil {
		// No preferred directory could even be named; there is nothing to fall back
		// FROM, so report it as the failure it is rather than silently relocating.
		return nil, err
	}

	candidates := []string{requested}
	// The platform cache dir, when an explicit --cache-dir / MCP_1C_CACHE_DIR was the
	// one that failed. os.UserCacheDir() is where the cache lives by default and is
	// writable in every environment that has one.
	if base, uErr := os.UserCacheDir(); uErr == nil {
		candidates = append(candidates, filepath.Join(base, name))
	}
	candidates = append(candidates, filepath.Join(os.TempDir(), name))

	var firstErr error
	for _, dir := range candidates {
		f, path, oErr := openAppendLog(dir, filename)
		if oErr != nil {
			if firstErr == nil {
				firstErr = oErr
			}
			continue
		}
		t := &logTarget{file: f, path: path}
		if dir != requested {
			t.requested, t.cause = requested, firstErr
		}
		return t, nil
	}
	return nil, firstErr
}

// preferredLogDir names the directory a log SHOULD go in: the explicit cache dir
// when there is one, otherwise the platform cache dir for name.
func preferredLogDir(name, cacheDir string) (string, error) {
	if cacheDir != "" {
		return cacheDir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, name), nil
}

// openAppendLog opens dir/filename for appending, creating dir if needed, and
// starts the file over when it has already grown past logRollAtBytes.
func openAppendLog(dir, filename string) (*os.File, string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, filename)
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if st, err := os.Stat(path); err == nil && st.Size() >= logRollAtBytes {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return nil, "", err
	}
	return f, path, nil
}

// reportLogFallback records, IN THE LOG THAT WAS ACTUALLY OPENED, that it is not
// the one that was asked for. Call it after the slog handler is installed on t.
// Error level: the cache directory being unwritable is an operator-actionable
// condition, and this line is the only thing that names where the diagnostics for
// this run went.
func reportLogFallback(t *logTarget) {
	if t == nil || t.cause == nil {
		return
	}
	slog.Error("cannot write the log inside the requested directory; this run's diagnostics are in "+
		"a fallback file instead. Make that directory writable, or point --cache-dir "+
		"(MCP_1C_CACHE_DIR) somewhere this user can write.",
		"requested", t.requested, "using", t.path, "error", t.cause)
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
