package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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

const expectedExtensionVersion = "0.4.7"

func main() {
	// realStderr is fd 2 as the process was launched with, captured before any
	// line below can reassign os.Stderr.
	//
	// The pipe-mode redirect further down replaces os.Stderr for the whole
	// process, and every message written after it lands wherever the redirect
	// points: the log file, or os.DevNull when no log file could be opened at
	// all. That is correct for what the redirect exists to contain, which is
	// third-party library noise arriving during a live session (Issue #14). It
	// is wrong for a startup refusal, which is this process's last act before
	// os.Exit: there is no session to protect, nothing can restart-loop on a
	// message from a process that is refusing to start, and the person who typed
	// the command is the only reader who can act on it. Keeping the original
	// handle is what lets reportStartupFailure reach that reader.
	realStderr := os.Stderr

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
	stripDefaultRoles := flag.Bool("strip-default-roles", false, "Install mode: do not declare MCP_ОсновнаяРоль as a default role of the configuration. Use this when a Standard Subsystems configuration refuses to start a session with «в свойстве ОсновныеРоли ... указаны лишние роли». Cost: the service then answers only users who have been assigned MCP_ОсновнаяРоль explicitly.")
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
		if err := installer.Install(extension.Source, *installDB, *serverMode, *platformPath, *dbUser, *dbPassword, *platformVersion, *stripDefaultRoles); err != nil {
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

	// Stderr handling outside of --debug. The branches are ENUMERATED AND NOT
	// COUNTED, because the total was wrong twice running and the leaf it left out
	// both times is the LAST one below, the only branch that does NOT clear the
	// pipe, which is the outcome the whole block exists to prevent. A written total
	// over an adjacent if/else can only rot, and no figure is restated here for the
	// next reader to mistake for a current one.
	//   - effectiveTTY=true => show info logs and progress in terminal (v1.6.0
	//     behaviour).
	//   - effectiveTTY=false => redirect stderr to a file to protect strict MCP
	//     clients (Kilo Code 7.x, Issue #14) from any third-party stderr writes
	//     (v1.6.1 behaviour).
	//   - the log file cannot be opened => redirect to os.DevNull anyway, because
	//     the pipe must be cleared whether or not a log exists. This is the mode
	//     that runs on a full disk.
	//   - os.DevNull cannot be opened either => keep the real stderr and warn. This
	//     is the ONLY leaf that leaves third-party writes on the client pipe, and
	//     it appeared neither in the heading's count nor in the list below it.
	// --debug overrides all of them and writes everything to server.log at INFO level.
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

	// Отказ выносится ЗДЕСЬ, на границе флага, и только здесь.
	//
	// Проверяется cfg.BaseURL, а не *baseURL: адрес мог прийти из переменной
	// окружения, и проверка сырого флага пропустила бы его.
	//
	// Почему не внутри onec.NewClient: тот же конструктор вызывают с
	// внутренними схемами вида proxy://<база> и poll://local, которых никто не
	// вводил руками и которые никакой разбор адреса подтвердить не может.
	// Отказ там убрал бы эти вызовы. Здесь же значение пришло от человека, и
	// человеку есть что с ним сделать.
	//
	// Сообщение не содержит ни одного байта значения: оно уходит и в настоящий
	// stderr, и в файл журнала, а шаблон отчёта об ошибке просит приложить
	// именно этот вывод.
	//
	// Доставку выполняет reportStartupFailure, а не Fprintln по os.Stderr:
	// к этому месту os.Stderr в режиме канала уже не файловый дескриптор 2, и
	// написанное туда сообщение пользователь не увидит вовсе.
	if err := onec.CheckURLCredentialResidue(cfg.BaseURL); err != nil {
		reportStartupFailure(realStderr, err)
		os.Exit(1)
	}

	client := onec.NewClient(cfg.BaseURL, cfg.User, cfg.Password,
		onec.WithMaxResponseSize(cfg.MaxResponseSizeMiB),
		onec.WithRequestTimeout(cfg.RequestTimeout),
	)

	// serveBuildCtx bounds any background generation build kicked off by
	// openServeIndexLocal so a build still in flight when serving ends cannot wedge
	// process exit. It is cancelled explicitly right after s.Run returns (and via the
	// defer as a backstop) so the deferred dumpIndex.Close() does not block on it.
	serveBuildCtx, serveBuildCancel := context.WithCancel(context.Background())
	defer serveBuildCancel()

	var dumpIndex *dump.Index
	if *dumpDir != "" {
		// Say it HERE, synchronously, before serving starts.
		//
		// The build that discovers the same fault runs in a background goroutine
		// and nothing sequences it against the end of the process, so its report is
		// not guaranteed to be written at all: measured over repeated starts it goes
		// missing outright, and on the serve-failure exit os.Exit skips the deferred
		// dumpIndex.Close that would otherwise wait for it. A diagnosis an operator
		// receives only most of the time is not a diagnosis.
		//
		// NOT fatal, deliberately. The tools that need the dump already answer with
		// an error naming this path, nothing else depends on it, and an unreadable
		// path is also what a not-yet-mounted share looks like, so refusing to start
		// would turn a degraded server into a restart loop. Exit stays 0.
		//
		// slog rather than a write to a descriptor: it is already pointed at the
		// right place for the launch mode, which is fd 2 on a terminal where the
		// person who typed the flag is watching, and the log file under a pipe,
		// where writing to fd 2 mid-session is the very thing the redirect exists
		// to prevent. ERROR because the pipe-mode handler keeps nothing below it.
		if err := dumpPathFault(*dumpDir); err != nil {
			slog.Error("--dump does not point at a usable directory, so search_code and "+
				"code_read will report a build error for the whole run. Every other tool is "+
				"unaffected and the server is starting normally. Correct the path and restart.",
				"dump", *dumpDir, "error", err)
		}

		// The SECOND cheap question, asked in the same place and for the same
		// reason: the path exists and is a directory, but is it the directory the
		// operator meant. A customer pointed --dump one level too high, at a folder
		// holding two dump roots, and nothing said so. Answering here, synchronously,
		// puts it in front of the person who typed the flag; answering from the build
		// goroutine would put it in a report that is not sequenced against the end of
		// the process and goes missing outright over repeated starts.
		reportDumpRootAndLayout(*dumpDir)

		var err error
		dumpIndex, err = openServeIndexLocal(serveBuildCtx, *dumpDir, *cacheDir, *reindex)
		if err != nil {
			// UNREACHABLE TODAY, and kept anyway. openServeIndexLocal returns an
			// error only by handing back dump.NewIndex's, and NewIndex has exactly
			// three return statements, every one of them "return idx, nil". So no
			// input reaches this branch, which is why there is no runtime test for
			// it: such a test could not fail, and a test that cannot fail reads as
			// evidence while proving nothing.
			//
			// Discarding the error instead would be worse. The signature returns
			// one, the day NewIndex grows a failing path this branch starts firing,
			// and a raw Fprintf here would write to a descriptor that is no longer
			// fd 2 and lose the message exactly when it finally matters. Delivery
			// is correct now, so the trap is disarmed before it is armed.
			reportStartupFailure(realStderr, fmt.Errorf("loading dump from %s: %w", *dumpDir, err))
			os.Exit(1)
		}
		defer dumpIndex.Close()
		// Index prepares in the background. ModuleCount is available after Ready().
	}

	// The extension version probe runs CONCURRENTLY with serving and is SEQUENCED
	// against the end of it. Both halves are the point.
	//
	// Concurrent, because synchronous would put a network round trip, up to the
	// probe's own three second deadline, in front of the MCP initialize handshake.
	// Keeping that handshake immediate is why the dump index builds in the
	// background at all (issue #30), and an unreachable 1C must not be able to
	// stall a client's first call.
	//
	// Sequenced, because a bare `go checkExtensionVersion(client)` is what made
	// the probe's silence meaningless. Nothing ordered that goroutine against the
	// end of the process, so a session that ended before the round trip finished
	// left a log byte for byte identical to a healthy run: measured on the real
	// binary against a loopback responder, stdin at EOF gave a 12 ms lifetime, an
	// empty log and ZERO requests on the wire. "No fault was reported" then meant
	// either "checked and fine" or "never checked", which is the exact defect the
	// probe was rewritten to remove one level up.
	//
	// It is started HERE rather than beside the client so that no exit between the
	// two can skip the wait below.
	versionCtx, versionCancel := context.WithCancel(context.Background())
	defer versionCancel()
	versionDone := make(chan struct{})
	go func() {
		defer close(versionDone)
		checkExtensionVersion(versionCtx, client)
	}()

	s := server.New(version, client, dumpIndex)

	runErr := s.Run(context.Background(), &mcp.StdioTransport{})
	// Serving has ended: stop any background build now so it cannot wedge shutdown
	// (the deferred dumpIndex.Close() waits on the index's Done()).
	serveBuildCancel()

	// Collect the probe's verdict before this process can end.
	//
	// A GRACE, not an immediate cancellation and not an unbounded wait. Cancelling
	// the moment serving stops is enough to make the log honest, but it also
	// throws away the answer: measured, the goroutine had not even reached the
	// wire on a short session, so every such run reported "did not finish" for a
	// 1C that was about to answer, and an ERROR line nobody can act on is how a
	// log stops being read. Waiting for the probe's own three second deadline
	// instead would put that on shutdown, where a supervisor may be waiting to
	// restart. So: wait a little, then cut it short and say so.
	//
	// The window is not a guess. Against a loopback responder the whole /version
	// round trip is 1.19 ms at worst over 40 runs and a refused connection turns
	// around in 0.27 ms, so versionProbeGrace is some eight hundred times the work
	// it waits on, and it elapses only when 1C accepted the connection and then
	// went quiet.
	select {
	case <-versionDone:
	case <-time.After(versionProbeGrace):
		versionCancel()
		<-versionDone
	}
	versionCancel()

	if runErr != nil {
		// Reached by any byte on stdin the JSON-RPC transport cannot decode, so
		// this is an everyday exit, not an exotic one. Same delivery as the startup
		// refusal and for the same reason: os.Stderr has been the log file since the
		// redirect, and a process that is exiting has no session left to protect, so
		// the one reader who can act on this must get it on the descriptor they are
		// actually watching.
		reportStartupFailure(realStderr, fmt.Errorf("mcp-1c error: %w", runErr))
		os.Exit(1)
	}
}

// reportStartupFailure delivers a message that must reach a human, on a path
// where os.Stderr may no longer be file descriptor 2.
//
// BOTH channels, deliberately, and the two readers are different people. fd 2 is
// what an MCP client surfaces to the user who launched the server, and it is the
// ONLY channel that still exists when no log file could be opened: in that
// configuration os.Stderr is os.DevNull, so a message written there is not
// misfiled, it is destroyed, and exit 1 becomes recoverable from nothing at all.
// The log is where the bug report template tells the user to look, it survives a
// client that swallows stderr, and it is what a later reader has. Dropping
// either one loses a real reader.
//
// The duplicate is suppressed when the two are the same handle, which is the
// terminal and --debug case, so nobody reads the refusal twice.
//
// NEVER fd 1. Stdout carries the JSON-RPC stream, and a byte of prose in front
// of the first frame breaks the transport for every client that reads it.
func reportStartupFailure(realStderr *os.File, err error) {
	fmt.Fprintln(realStderr, err)
	if os.Stderr != realStderr {
		fmt.Fprintln(os.Stderr, err)
	}
}

// logRollAtBytes caps a log file's carried-over history. Below it the file is
// APPENDED to (see openLogFile); at or above it the next process to open the file
// starts it over, so the disk cost stays bounded without a rotation scheme and
// without the race a rename between co-located processes would add.
const logRollAtBytes = 8 << 20 // 8 MiB

// logTarget is the log file a run actually got, plus what it asked for. requested
// and cause are set ONLY when the requested directory could not be used, so a
// non-nil cause is the signal to report the substitution, into the file itself.
//
// The file is where the substitution is reported because a substitution is a
// detail of a run that is otherwise proceeding normally, and a live stdio session
// is exactly what the redirect exists to keep quiet. It is NOT, as this comment
// used to claim, "the only place a stdio-mode process can report anything": the
// process still holds file descriptor 2, and main keeps it in realStderr for the
// case where a message has to reach a person rather than a log. Believing the
// stronger claim is what let a startup refusal ship as exit 1 with zero bytes on
// both descriptors.
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

// dumpPathFault reports why dumpDir cannot be a DumpConfigToFiles directory, or
// nil when it can be one.
//
// It answers the cheap question only: does the path exist and is it a directory.
// Whether the contents are a valid dump is settled by the index build, which is
// asynchronous for a reason (a large dump must not delay the MCP initialize
// handshake). A stat costs microseconds and cannot delay anything, which is what
// makes it safe to do before serving starts.
func dumpPathFault(dumpDir string) error {
	st, err := os.Stat(dumpDir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", dumpDir)
	}
	return nil
}

// nestedDumpRootMessage is what to say when --dump does not point at the dump
// root, or "" when there is nothing to say.
//
// SILENCE IS THE NORMAL ANSWER and is the shipped behaviour: a path that is itself
// a dump root produces nothing at all.
//
// IT REPORTS WHAT WAS MEASURED, AND STOPS THERE. The version this replaces asserted
// that «модули расширения и конфигурации попадают в одно пространство ключей и
// затирают друг друга», and on the tree that motivated the whole feature that
// sentence is FALSE: two extension dumps side by side are recognised from their
// manifests, keyed as ext.<Имя>., and the same process measures the overwrite as
// zero. It then told the operator to point --dump at ONE of the roots, which for
// that tree means throwing the other extension away. A message that misstates the
// harm and prescribes a loss is worse than no message, so this one says which of
// the children the server recognised and what it will do with them, and warns
// about a shared keyspace only when the children are NOT extensions and really do
// share one, which takes AT LEAST TWO of them. One root below the path shared a
// keyspace with nothing and was told it overwrote itself; that branch is now its
// own and is documented at the case that carries it.
//
// IT NAMES NOTHING. Directory names are read off disk and a real customer tree
// contains «Доработки — копия», so a name spliced into this sentence puts a тире in
// customer-facing RU no matter what the guard on the sentence says. The names ride
// in the slog attributes beside it, which is where data belongs and where the
// operator can still read them.
//
// Customer-facing RU: no тире.
func nestedDumpRootMessage(insp dump.DumpRootInspection, layout dump.ExtensionLayoutSummary) string {
	if insp.RootIsSymlink {
		// FIRST, AND IT OVERRIDES EVERYTHING BELOW. The inspection followed the link
		// and may be about to report a perfectly good dump root; the indexer will not
		// follow it and will index nothing. Measured: a symlink to a two-module dump
		// inspects as a root and yields 0 modules. Every other sentence in this
		// function would be true of the target and useless about the path.
		return "путь в --dump это символьная ссылка. Индексатор внутрь ссылки не " +
			"заходит, поэтому в индекс не попадёт ни один модуль, даже если по ссылке " +
			"лежит правильная выгрузка. Укажите настоящий путь к каталогу выгрузки."
	}
	if insp.IsRoot {
		if insp.SymlinkedChildren > 0 {
			// A root that is correct, with children the walk will skip. Worth one
			// sentence and not the one above: what is lost is those subtrees, not
			// everything.
			return "путь в --dump указывает на корень выгрузки, но среди его подкаталогов " +
				"есть символьные ссылки, штук: " + strconv.Itoa(insp.SymlinkedChildren) +
				". Индексатор внутрь ссылок не заходит, поэтому их содержимое в индекс не " +
				"попадёт. Если модули лежат по ссылке, укажите настоящий путь."
		}
		return ""
	}
	if len(insp.NestedRoots) == 0 {
		// NOTHING BELOW LOOKS LIKE A ROOT EITHER, AND THIS SAYS NOTHING ABOUT IT.
		// The tempting sentence here is «путь не выглядит корнем выгрузки», and one
		// ReadDir cannot support it: a hand-made tree holding only CommonModules
		// scores below minKindDirsForRoot and is indistinguishable, at this cost,
		// from a path two levels above a real root, and its keys are perfectly
		// correct. Guessing would put a warning in front of every operator with a
		// partial but valid tree.
		//
		// The case that matters here, a --dump two levels too high, is reported on
		// the other channel and by MEASUREMENT rather than by shape: every file in
		// such a tree is keyed from a path the anchor scan had to move, which is
		// what dump.WrappedPathState counts and what the notice in
		// tools/index_notice.go carries. That number is zero for the partial tree
		// above and is every file for this one.
		return ""
	}

	msg := "путь в --dump указывает не на корень выгрузки, а на каталог выше него. "
	if len(insp.NestedRoots) == 1 {
		msg += "Внутри лежит готовый корень выгрузки (имя в поле roots). "
	} else {
		msg += "Внутри лежат готовые корни выгрузки, " + strconv.Itoa(len(insp.NestedRoots)) +
			" штуки, их имена в поле roots. "
	}

	switch {
	case len(insp.NestedRoots) == 1:
		// ONE ROOT BELOW THE PATH, AND THERE IS NOTHING FOR ITS MODULES TO OVERWRITE.
		// This case used to fall to the default branch and assert that «их модули
		// попадают в одно пространство ключей и затирают друг друга», which needs a
		// second root to be true of anything. Measured on such a tree, in the test
		// beside this one: the anchor scan moves the anchor onto the kind directory
		// inside the root, the keys come out as they would from the root itself, and
		// the collapse the tool-output notice carries is 0 files over every module
		// indexed. The notice on the other channel is careful for exactly that
		// reason, because it prints a number it measured; this one printed a claim.
		//
		// So this branch reports the PATH, which is what was observed, and says
		// nothing about loss in either direction: an inspection that read one ReadDir
		// has not measured what the index will do, and «ничего не потеряется» would
		// be the same kind of sentence as the one being removed, only reassuring.
		if layout.Extensions == 1 {
			msg += "Он опознан как выгрузка расширения, и сервер проиндексирует его под " +
				"собственным именем, так что содержимое не потеряется. Указывать его в " +
				"--dump нужно только если вам нужен именно он."
		} else {
			msg += "Он не опознан как выгрузка расширения. Укажите в --dump сам этот корень."
		}
	case layout.Extensions >= len(insp.NestedRoots):
		// Every root below the path is a recognised extension. Their modules get
		// their own namespace, so nothing collides and nothing is lost; saying so is
		// what stops the operator from re-pointing the path and discarding the rest.
		msg += "Все они опознаны как выгрузки расширений, и сервер проиндексирует " +
			"каждую под её собственным именем, так что содержимое не потеряется. " +
			"Указывать один из них в --dump нужно только если вам нужен именно он."
	case layout.Extensions > 0:
		msg += "Часть из них опознана как выгрузки расширений и получит собственные " +
			"имена, остальные попадут в общее пространство ключей и могут затереть " +
			"друг друга. Укажите в --dump тот корень, который вам нужен."
	default:
		msg += "Ни один из них не опознан как выгрузка расширения, поэтому их модули " +
			"попадают в одно пространство ключей и затирают друг друга. Укажите в " +
			"--dump тот корень, который вам нужен."
	}

	msg += " Сервер не переходит внутрь сам, потому что выбрать за вас не может, " +
		"а молчаливый переход скрыл бы ошибку пути ровно так же, как она скрывалась до сих пор."
	if insp.Truncated {
		msg += " Просмотрены не все подкаталоги, поэтому список может быть неполным."
	}
	return msg
}

// extensionLayoutDoubtMessage is what to say about directories whose
// extension-ness the server could not decide, or "" when it decided every one.
//
// A DOUBT IS NEVER A GUESS AND IS NEVER SILENCE. Detection has three answers, and
// the third one leaves the keys exactly as they were before extensions were
// recognised at all. That is the safe direction, and it is only safe because it is
// said out loud: a namespace that quietly failed to appear looks identical to a
// dump that never had one.
//
// Counts only, no names, for the reason nestedDumpRootMessage gives.
//
// Customer-facing RU: no тире.
func extensionLayoutDoubtMessage(layout dump.ExtensionLayoutSummary) string {
	if layout.Undecided() == 0 && !layout.ScanTruncated {
		return ""
	}
	msg := "часть каталогов выгрузки сервер не смог отнести к расширениям. " +
		"Их модули проиндексированы без имени расширения, то есть так же, как до " +
		"появления этой возможности."
	if n := layout.NotRegular; n > 0 {
		msg += " Файл Configuration.xml оказался не обычным файлом, каталогов: " + strconv.Itoa(n) + "."
	}
	if n := layout.Unreadable; n > 0 {
		msg += " Не удалось прочитать Configuration.xml, каталогов: " + strconv.Itoa(n) + "."
	}
	if n := layout.ReadTruncated; n > 0 {
		msg += " Манифест не поместился в окно чтения, каталогов: " + strconv.Itoa(n) + "."
	}
	if n := layout.NameRejected; n > 0 {
		msg += " Объявленное имя расширения нельзя использовать как часть ключа, каталогов: " +
			strconv.Itoa(n) + "."
	}
	if n := layout.Malformed; n > 0 {
		msg += " В Configuration.xml не закрыт комментарий, блок CDATA или инструкция " +
			"обработки, каталогов: " + strconv.Itoa(n) + "."
	}
	if n := layout.Unscannable; n > 0 {
		msg += " В Configuration.xml есть объявление DOCTYPE или другое объявление " +
			"разметки, границы которого сервер не определяет, каталогов: " + strconv.Itoa(n) + "."
	}
	if layout.ScanTruncated {
		msg += " Просмотрены не все подкаталоги, поэтому расширения могли остаться незамеченными."
	}
	return msg
}

// reportDumpRootAndLayout delivers both messages, or says nothing.
//
// slog.Error, and the level is the whole delivery. main installs the default
// handler at LevelError (MCP clients render every stderr line as [error], so
// INFO and WARN are suppressed on purpose), which means a slog.Warn here would
// compile, would have a passing test, and would never appear in a normal run.
// That exact failure has been shipped before. The pin lives in
// nested_dump_root_test.go:TestNestedDumpRootNoticeIsDeliveredAtErrorLevel, which
// publishes through a handler that keeps nothing below Error, so a downgrade to
// Warn fails the build instead of going quiet.
//
// WHERE IT LANDS, MEASURED ON THE BUILT BINARY rather than argued: on a terminal
// launch it is written to file descriptor 2, and under a pipe launch it goes to
// stderr.log in the cache directory, because that is where main has pointed
// stderr by then and writing to fd 2 mid-session is the very thing that redirect
// exists to prevent.
//
// So under a pipe this line reaches a log, and a log nobody reads is not a
// message. That is accepted here because it is not the only channel these
// conditions have: the notices in tools/index_notice.go ride on EVERY tool
// response with the exact counts. This one adds the precise diagnosis for the
// operator who can see stderr, in the same place and by the same means as the
// dumpPathFault message it sits beside.
//
// ONE LAYOUT DETECTION, used by both messages, so the two cannot describe
// different readings of the same tree.
func reportDumpRootAndLayout(dumpDir string) {
	insp := dump.InspectDumpRoot(dumpDir)
	layout := dump.InspectExtensionLayout(dumpDir)

	if msg := nestedDumpRootMessage(insp, layout); msg != "" {
		slog.Error(msg, "dump", dumpDir,
			"roots", strings.Join(insp.NestedRoots, ","),
			"extensions", layout.Extensions,
			"extension_dirs", strings.Join(layout.Dirs, ","))
	}
	if msg := extensionLayoutDoubtMessage(layout); msg != "" {
		slog.Error(msg, "dump", dumpDir, "undecided", layout.Undecided(),
			"scan_truncated", layout.ScanTruncated)
	}
}

// compareExtensionVersions orders two dotted numeric extension versions,
// returning -1, 0 or 1, and ok=false when either side is not a version at all.
//
// Components are compared as NUMBERS. Lexicographic order gets 0.4.10 < 0.4.9
// and 0.10.0 < 0.9.9 wrong, and those are ordinary version numbers, not corner
// cases. A missing trailing component counts as zero, so 1.2.3 and 1.2.3.0 are
// the same version.
//
// A suffix that is not part of the dotted number (a pre-release marker such as
// the "-beta" in 0.5.0-beta) is trimmed rather than rejected: refusing to parse
// it would turn a perfectly readable version into the "cannot tell" branch and
// produce an alarm about a healthy extension. Ordering ignores the suffix, which
// is imprecise between two pre-releases of one version and irrelevant here,
// where the question is only whether the installed extension reaches a floor.
func compareExtensionVersions(a, b string) (int, bool) {
	an, aok := versionComponents(a)
	bn, bok := versionComponents(b)
	if !aok || !bok {
		return 0, false
	}
	for i := 0; i < len(an) || i < len(bn); i++ {
		var av, bv int
		if i < len(an) {
			av = an[i]
		}
		if i < len(bn) {
			bv = bn[i]
		}
		if av != bv {
			if av < bv {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// maxVersionTextBytes bounds the ONE value the /version probe takes from the far
// side, both for parsing and for the log.
//
// It is not a style limit, it is a budget. The answer arrives under the client's
// response cap, 128 MiB by default (config.DefaultMaxResponseSizeMiB), and both
// things this code then does with the value scale with its length:
//
//   - versionComponents splits it into components and keeps an int per
//     component. Measured on this repository before this bound: 1 MiB of "1."
//     pairs allocated 28.1 MiB, 4 MiB allocated 129.9 MiB, 16 MiB allocated
//     503.2 MiB, i.e. 28x to 32x, which puts a body at the cap in the gigabytes.
//     This runs on the probe goroutine, where running out of memory does not fail
//     one call, it takes the process down.
//   - the value is echoed into the log as "got", and openAppendLog restarts a log
//     file that has reached logRollAtBytes (8 MiB). One oversized answer would
//     therefore make the NEXT start discard the operator's whole history.
//
// The number is set against what a version actually is. The longest this
// repository produces or names are the extension's own 0.4.7 and platform builds
// like 8.3.27.2130, eleven bytes; a pre-release suffix such as 0.5.0-beta adds a
// few more. 128 bytes is an order of magnitude above the longest of those and
// still one line in a log. A value over it is not shortened into a version, it is
// REFUSED, which routes it to the "cannot be ranked" outcome that already exists
// and already says the answer is not a version number.
const maxVersionTextBytes = 128

// versionComponents splits a dotted numeric version into its components,
// stopping at the first character that cannot belong to one. It reports false
// when there is no leading number at all, which is what an empty string, an
// HTML page or a foreign JSON body produces, and when the value is longer than
// maxVersionTextBytes, which no version is.
//
// The length is checked FIRST, before any allocation. Checking it after the split
// would be checking it after the cost.
func versionComponents(v string) ([]int, bool) {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > maxVersionTextBytes {
		return nil, false
	}
	// Cut any pre-release / build suffix: keep the leading run of digits and dots.
	end := strings.IndexFunc(v, func(r rune) bool {
		return r != '.' && (r < '0' || r > '9')
	})
	if end >= 0 {
		v = v[:end]
	}
	v = strings.TrimRight(v, ".")
	if v == "" {
		return nil, false
	}
	var out []int
	for _, part := range strings.Split(v, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// versionForLog bounds a far-side value on its way into the log, and says so
// when it cuts.
//
// The cut is ANNOUNCED and carries the full length. A silent cut is read as the
// whole value, and "the extension answered 999…9" is a different diagnosis from
// "the extension answered two megabytes of nines"; the second one names the
// fault. slog quotes the attribute, so the value cannot forge a line either way.
//
// The tail of a rune split by the byte cap is dropped rather than written: a
// half-rune reaches the reader as U+FFFD and looks like something the far side
// sent.
func versionForLog(v string) string {
	if len(v) <= maxVersionTextBytes {
		return v
	}
	cut := v[:maxVersionTextBytes]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size != 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return fmt.Sprintf("%s… (truncated, %d bytes in the answer)", cut, len(v))
}

// versionProbeGrace is how long the end of a session waits for the extension
// version probe before cutting it short. See the wait in main for what the
// number rests on.
const versionProbeGrace = time.Second

// checkExtensionVersion reports EXACTLY ONE verdict per call, on every path, and
// returns only after it has been logged. That is what lets main treat the return
// as the end of the check.
//
// ctx belongs to the caller and is cancelled when serving ends; the three second
// deadline is this function's own and bounds a 1C that accepts the connection and
// then says nothing.
func checkExtensionVersion(ctx context.Context, client *onec.Client) {
	sessionEnded := ctx.Done()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var ver onec.VersionInfo
	if err := client.Get(ctx, "/version", &ver); err != nil {
		// The session ending first is a DIFFERENT outcome and gets its own words.
		// Reusing the line below would swap one false statement for another: it
		// names a wrong publication path, refused credentials, a web server in
		// the way or a stopped 1C, and none of those is what happened when the
		// reader's own client disconnected before the round trip finished.
		select {
		case <-sessionEnded:
			slog.Error("Extension version NOT verified: the session ended before /version "+
				"answered, so the check did not finish. This says nothing about the installed "+
				"extension either way. It is ordinary for a very short session; if it repeats "+
				"on sessions that do real work, 1C is answering slowly.",
				"required_at_least", expectedExtensionVersion, "deadline", 3*time.Second)
			return
		default:
		}
		// NOT a silent skip. Returning without a word here is what made a failed
		// probe indistinguishable from a healthy one: both produced a completely
		// empty log, so "no mismatch was reported" was not evidence of a match,
		// it was evidence of nothing.
		slog.Error("Extension version NOT verified: /version did not answer. This is not a "+
			"confirmation that the installed extension is the right one; the check did not "+
			"run at all. Usual causes: the publication path is wrong, the credentials were "+
			"refused, a web server answered instead of 1C, or 1C is not running.",
			"required_at_least", expectedExtensionVersion, "error", err)
		return
	}

	got := strings.TrimSpace(ver.Version)
	// shown is what any line below may put in front of a reader. got stays whole
	// for the comparison, which bounds itself; the LOG does not, and it is a file
	// openAppendLog restarts once it reaches logRollAtBytes.
	shown := versionForLog(got)
	if got == "" {
		// An answer that carries no version is not a version mismatch. The old code
		// let the zero value fall through to the comparison and reported got="",
		// which reads as a measurement of the installed extension when in fact
		// nothing was ever measured.
		slog.Error("Extension version NOT verified: /version answered without a version. "+
			"Something is listening at that address, but it is not the MCP extension this "+
			"binary talks to.",
			"required_at_least", expectedExtensionVersion)
		return
	}

	order, ok := compareExtensionVersions(got, expectedExtensionVersion)
	if !ok {
		slog.Error("Extension version NOT verified: /version answered with something that is "+
			"not a version number, so it cannot be ranked against what this binary requires.",
			"got", shown, "required_at_least", expectedExtensionVersion)
		return
	}

	if order < 0 {
		// The one genuine fault. Extension releases ADD endpoints the Go side calls
		// (/subsystems arrived in ext 0.4.3), so below the floor an endpoint this
		// binary uses may simply not be there.
		//
		// This is also the only outcome where --install is the remedy, so it is the
		// only one that mentions it, and it says which version it would install:
		// the advice used to be given for every difference including a NEWER
		// extension, where following it would overwrite a working install with an
		// older one.
		slog.Error("Extension is OLDER than this build requires, so endpoints it calls may be "+
			"missing and some tools will fail.",
			"got", shown, "required_at_least", expectedExtensionVersion,
			"hint", `reinstall the bundled extension `+expectedExtensionVersion+
				`: mcp-1c --install "path\to\db"`)
		return
	}

	// order >= 0: every endpoint this binary calls exists in the installed
	// extension, so there is no fault to report.
	//
	// A HIGHER number is a supported deployment, not a problem: the paid editions
	// ship a further-along extension and pairing it with this binary is guaranteed
	// to work. Comparing for EQUALITY is what turned that healthy configuration
	// into an ERROR line on every start, which is the exact habit that teaches an
	// operator to stop reading the log.
	//
	// Nothing here names an edition, and it must not: ВерсияGET returns one key
	// holding a bare version string, and onec.VersionInfo has one field to receive
	// it, so which product built the installed extension is not observable from
	// this side. Ordering is the only thing the answer supports.
	//
	// INFO, not ERROR, and that is what makes silence meaningful. In pipe mode the
	// handler sits at LevelError, so this line is filtered and a quiet log means
	// exactly one thing: the probe ran and was satisfied. Every other outcome above
	// is ERROR and survives the filter. On a terminal or under --debug the levels
	// are not filtered and the confirmation is visible directly.
	//
	// THAT SENTENCE IS ONLY TRUE BECAUSE MAIN WAITS. It was written while the probe
	// was launched with a bare `go`, and then a quiet log also meant "the process
	// ended before the round trip", which on a short session is what it usually
	// meant: measured on the real binary, stdin at EOF gave a 12 ms lifetime, an
	// empty log and zero requests on the wire. Silence discriminates only while
	// every path out of main collects this goroutine first, which is asserted by
	// TestVersionProbe_VerdictSurvivesASessionThatEndsFirst against the request
	// count, not against the log.
	if order > 0 {
		slog.Info("Extension version verified; it is newer than the one this build bundles, "+
			"which is a supported combination.",
			"got", shown, "bundled", expectedExtensionVersion)
		return
	}
	slog.Info("Extension version verified.", "got", shown)
}
