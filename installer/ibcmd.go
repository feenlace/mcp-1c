// Package installer — ibcmd backend for headless extension deployment.
//
// ibcmd is a CLI utility shipped with 1С:Enterprise platform since 8.3.18.
// Compared to DESIGNER batch mode it offers:
//   - ~30 seconds per install vs 3-8 minutes (no GUI startup overhead)
//   - No GUI window (no Session 0 issues on Windows Server)
//   - Native Linux support (DESIGNER has limitations there)
//
// Gotchas codified here (from real-world deploys to cluster-managed PostgreSQL infobases):
//
//  1. --db-server="host port=6432" — NO trailing semicolon despite docs claim.
//     With trailing ";", ibcmd misparses and falls back to default port 5432.
//
//  2. --user X requires --password Y paired. Else ibcmd jumps to interactive
//     --request-db-pwd mode reading from STDIN. With redirected/closed STDIN
//     this hangs forever (process visible, 0% CPU, no network).
//
//  3. --data dir must be cleaned between operations on shared file storage
//     to avoid stale lock files.
//
//  4. config import files needs exclusive lock on infobase metadata.
//     If extension is actively used by rphost sessions, ibcmd waits forever.
//     We enforce a timeout (default 60s) with actionable error message.
package installer

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// IbcmdOptions configures the ibcmd-based installer.
type IbcmdOptions struct {
	// DBPath is the database location. In server mode: "server\database".
	// In file mode: filesystem path. Used to derive --dbms / --db-server / --db-name.
	DBPath string

	// ServerMode true => treat DBPath as "server\database" client-server connection.
	// false => file-mode infobase. ibcmd uses different flags accordingly.
	ServerMode bool

	// PlatformExe is the path to 1cv8.exe (or 1cv8c). Used to locate ibcmd
	// next to it. If empty, FindPlatform() is called.
	PlatformExe string

	// DBUser, DBPassword authenticate to the DBMS (PostgreSQL/MSSQL user).
	// Required when ServerMode is true. Empty in file mode.
	DBUser     string
	DBPassword string

	// PlatformVersion override (e.g. "8.3.27"). If empty, detected from PlatformExe.
	PlatformVersion string

	// Timeout per ibcmd invocation. Default 60s. Increase if expecting lock
	// contention with active rphost sessions.
	Timeout time.Duration

	// DataDir is ibcmd's --data working directory. If empty, a temp dir is
	// created and cleaned up. Useful to override for debugging.
	DataDir string

	// DBMS — "PostgreSQL" or "MSSQLServer" or "IBMDB2" or "OracleDatabase".
	// If empty in ServerMode, defaults to PostgreSQL (most common for 1С on Linux).
	DBMS string

	// DBServer overrides the server part of DBPath. If both DBPath and DBServer
	// are set, DBServer wins. Useful for clusterized infobases where DBPath is
	// "ras-cluster:1541\db1" but actual PG host is different.
	DBServer string
}

// defaultIbcmdTimeout is the fallback per-call timeout for ibcmd. 60 seconds
// covers normal config import + apply on idle bases. For busy production bases
// with active rphost sessions, caller should bump this to 300+.
const defaultIbcmdTimeout = 60 * time.Second

// InstallViaIbcmd deploys the embedded extension XML sources to the target
// infobase via headless ibcmd.
//
// Steps:
//  1. Extract embedded XML to temp dir
//  2. Patch XML format version for platform compat (shared with DESIGNER path)
//  3. Find ibcmd executable next to platform binary
//  4. Run `ibcmd config import files --extension=X --base-dir=tmp ...`
//  5. Run `ibcmd config apply --extension=X --dynamic=auto ...`
//  6. Cleanup
//
// Returns nil on success, descriptive error on any step failure.
//
//garble:ignore
func InstallViaIbcmd(srcFS embed.FS, opts IbcmdOptions) error {
	if opts.Timeout == 0 {
		opts.Timeout = defaultIbcmdTimeout
	}

	// Resolve platform exe (shared logic with DESIGNER installer).
	if opts.PlatformExe == "" {
		exe, err := FindPlatform()
		if err != nil {
			return fmt.Errorf("finding 1C platform: %w", err)
		}
		opts.PlatformExe = exe
	}
	fmt.Printf("Platform: %s\n", opts.PlatformExe)

	// Find ibcmd next to platform exe.
	ibcmdPath, err := findIbcmd(opts.PlatformExe)
	if err != nil {
		return fmt.Errorf("finding ibcmd: %w (requires platform 8.3.18+)", err)
	}
	fmt.Printf("ibcmd: %s\n", ibcmdPath)

	// Extract extension XML to temp dir (same as DESIGNER path).
	extDir, err := os.MkdirTemp("", "mcp-1c-ext-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(extDir)

	if err := extractFS(srcFS, "src", extDir); err != nil {
		return fmt.Errorf("extracting extension sources: %w", err)
	}

	// Patch XML format version to match the target platform.
	fmtVer := formatVersionForPlatform(opts.PlatformExe)
	if err := patchFormatVersion(extDir, fmtVer); err != nil {
		return fmt.Errorf("patching format version: %w", err)
	}

	// Allocate ibcmd data dir.
	dataDir := opts.DataDir
	if dataDir == "" {
		dataDir, err = os.MkdirTemp("", "mcp-1c-ibcmd-data-*")
		if err != nil {
			return fmt.Errorf("creating ibcmd data dir: %w", err)
		}
		defer os.RemoveAll(dataDir)
	}

	// Build the connection args. These are shared by all ibcmd invocations.
	connArgs, err := buildConnectionArgs(opts)
	if err != nil {
		return fmt.Errorf("building connection args: %w", err)
	}

	// Step 1: import XML into config storage.
	//
	// Note: `ibcmd config import <path>` imports the whole directory.
	// `ibcmd config import files` is for selective per-file imports
	// (with positional <File1 ... FileN>), not what we want here.
	fmt.Println("Importing extension XML via ibcmd config import...")
	importArgs := append([]string{
		"config", "import",
		"--extension=" + extensionName,
		"--data=" + dataDir,
	}, connArgs...)
	importArgs = append(importArgs, extDir) // positional <path> arg
	if err := runIbcmd(context.Background(), ibcmdPath, importArgs, opts.Timeout); err != nil {
		return classifyIbcmdError("config import", err, opts.Timeout)
	}

	// Step 2: apply config to DB.
	fmt.Println("Applying extension config via ibcmd config apply --dynamic=auto...")
	applyArgs := append([]string{
		"config", "apply",
		"--extension=" + extensionName,
		"--dynamic=auto",
		"--data=" + dataDir,
	}, connArgs...)
	if err := runIbcmd(context.Background(), ibcmdPath, applyArgs, opts.Timeout); err != nil {
		return classifyIbcmdError("config apply", err, opts.Timeout)
	}

	fmt.Println("Extension installed successfully via ibcmd.")
	return nil
}

// findIbcmd looks for ibcmd executable next to the platform binary.
// On Windows: ibcmd.exe in the same directory. On Linux: ibcmd (no extension).
//
// If platformExe is something like "C:\Program Files\1cv8\8.3.27.1859\bin\1cv8.exe",
// we expect ibcmd at "C:\Program Files\1cv8\8.3.27.1859\bin\ibcmd.exe".
func findIbcmd(platformExe string) (string, error) {
	binDir := filepath.Dir(platformExe)
	name := "ibcmd"
	if runtime.GOOS == "windows" {
		name = "ibcmd.exe"
	}
	candidate := filepath.Join(binDir, name)
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	// Fallback: search PATH.
	if found, err := exec.LookPath(name); err == nil {
		return found, nil
	}
	return "", fmt.Errorf("ibcmd not found next to %s nor in PATH", platformExe)
}

// buildConnectionArgs builds the DBMS connection args for ibcmd.
//
// Key design point: ibcmd bypasses the 1C cluster and connects directly to
// the DBMS (PostgreSQL/MSSQL). For cluster-managed infobases, the DESIGNER
// cluster path "cluster:1541\db" does NOT map to ibcmd flags — the user must
// provide the actual DBMS host via DBServer (--ibcmd-db-server flag).
//
// CRITICAL gotcha: --db-server="host port=6432" — NO trailing semicolon despite
// docs claim. With ";", ibcmd silently uses default port 5432.
func buildConnectionArgs(opts IbcmdOptions) ([]string, error) {
	args := []string{}
	if !opts.ServerMode {
		// File-mode infobase.
		args = append(args, "--db-path="+opts.DBPath)
		return args, nil
	}

	// Client-server mode: ibcmd needs explicit DBMS server + DB name.
	//
	// Two acceptable inputs:
	//   1) Explicit DBServer + DBPath as plain DB name (preferred for ibcmd)
	//   2) DBPath in DESIGNER "server\db" format, DBServer unset — we split
	//      and use parts[0] as DBMS host. WARNING: this works only if user
	//      put the actual DBMS host in DBPath (not 1C cluster name).
	dbServer := opts.DBServer
	dbName := ""
	if dbServer != "" && !strings.Contains(opts.DBPath, `\`) {
		// Preferred path: DBServer explicit, DBPath is plain DB name.
		dbName = opts.DBPath
	} else {
		// Legacy split: parse "ServerName\DBName" from DBPath.
		parts := strings.SplitN(opts.DBPath, `\`, 2)
		if len(parts) == 2 {
			if dbServer == "" {
				dbServer = parts[0]
			}
			dbName = parts[1]
		} else {
			return nil, fmt.Errorf(
				"--installer=ibcmd with --server requires either "+
					"(a) --ibcmd-db-server <DBMS-host> and --install <DB-name>, OR "+
					"(b) --install <DBMS-host>\\<DB-name>. Got DBPath=%q, DBServer=%q. "+
					"Note: ibcmd connects directly to DBMS, NOT through 1C cluster — "+
					"the DESIGNER cluster path syntax (cluster:1541\\db) does NOT work for ibcmd",
				opts.DBPath, opts.DBServer,
			)
		}
	}

	dbms := opts.DBMS
	if dbms == "" {
		dbms = "PostgreSQL"
	}
	args = append(args,
		"--dbms="+dbms,
		// IMPORTANT: no trailing ";" after port=N (docs are wrong)
		"--db-server="+dbServer,
		"--db-name="+dbName,
	)

	// IMPORTANT: --user/--password MUST be paired. Single --user without
	// --password causes ibcmd to enter interactive STDIN read mode and hang.
	if opts.DBUser != "" {
		if opts.DBPassword == "" {
			return nil, errors.New("DBUser set but DBPassword empty — would hang on STDIN")
		}
		args = append(args, "--db-user="+opts.DBUser, "--db-pwd="+opts.DBPassword)
	}

	return args, nil
}

// runIbcmd executes ibcmd with the given args and a hard timeout.
//
// Closes stdin explicitly to prevent ibcmd's interactive password prompt
// from waiting forever when it can't parse argv credentials.
func runIbcmd(parent context.Context, ibcmdPath string, args []string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, ibcmdPath, args...)
	// Explicitly nil stdin — ibcmd reads STDIN only when interactive password
	// prompt is triggered; a nil stdin returns EOF immediately rather than
	// blocking on terminal read.
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("ibcmd timed out after %s\noutput: %s", timeout, decodeForLog(out))
	}
	if err != nil {
		return fmt.Errorf("ibcmd %s: %w\noutput: %s", strings.Join(args[:min(3, len(args))], " "), err, decodeForLog(out))
	}
	// ibcmd prints "[INFO] ..." lines on success; surface them to user.
	if len(out) > 0 {
		fmt.Println(decodeForLog(out))
	}
	return nil
}

// classifyIbcmdError converts low-level ibcmd errors into user-friendly messages
// with actionable hints.
func classifyIbcmdError(step string, err error, timeout time.Duration) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "timed out"):
		return fmt.Errorf("ibcmd %s timed out after %s — likely lock contention "+
			"(active rphost sessions hold extension metadata). "+
			"Try: (a) wait until base is idle and retry; "+
			"(b) increase --ibcmd-timeout; "+
			"(c) fall back to --installer=designer for busy bases.\nOriginal: %w", step, timeout, err)
	case strings.Contains(msg, "Connection refused"), strings.Contains(msg, "connection failed"):
		return fmt.Errorf("ibcmd %s: cannot reach database server. "+
			"Check --db-server hostname and port (use 'host port=N' WITHOUT trailing ';'). "+
			"For PostgreSQL: typical port is 5432 or 6432 (pgbouncer).\nOriginal: %w", step, err)
	case strings.Contains(msg, "authentication failed"), strings.Contains(msg, "не верный пароль"):
		return fmt.Errorf("ibcmd %s: authentication failed. "+
			"Check --db-user/--db-pwd for DBMS and --user/--password for infobase admin.\nOriginal: %w", step, err)
	default:
		return fmt.Errorf("ibcmd %s: %w", step, err)
	}
}

// decodeForLog ensures output is valid UTF-8 for display.
//
// ibcmd on Windows emits its log lines in the system OEM codepage when
// STDOUT is redirected — typically cp866 (for Russian Windows). If the
// raw bytes are not valid UTF-8, we try cp866 then windows-1251 as
// fallbacks. This mirrors the pattern used in installer.go for DESIGNER
// log files (Windows1251) but adds cp866 as the primary attempt because
// cmd.exe inherits OEM codepage for redirected output, not ANSI.
func decodeForLog(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	// Try cp866 first (cmd.exe redirected stdout default on RU Windows).
	if decoded, err := charmap.CodePage866.NewDecoder().Bytes(b); err == nil && utf8.Valid(decoded) {
		return string(decoded)
	}
	// Fallback to windows-1251 (some 1C output uses ANSI codepage).
	if decoded, err := charmap.Windows1251.NewDecoder().Bytes(b); err == nil && utf8.Valid(decoded) {
		return string(decoded)
	}
	// Last resort: best-effort string (may contain replacement chars).
	return string(b)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
