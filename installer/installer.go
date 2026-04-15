package installer

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

const extensionName = "MCP_HTTPService"

// defaultFormatVersion is the fallback XML dump format version used when the
// platform version cannot be detected. 2.7 is the format for platforms older
// than 8.3.14 (the lowest entry in platformFormatVersions).
const defaultFormatVersion = "2.7"

// platform85FormatVersion is the XML dump format version for 1C 8.5.x.
const platform85FormatVersion = "2.21"

// platformFormatVersions maps 1C platform minor versions to the XML dump format
// version they introduced. Platforms can load XML with format versions up to and
// including their own, but reject anything newer.
//
// Source: official 1C release notes (1cv8upd), each version states
// "Версия формата выгрузки конфигурации в XML-файлы стала равной X.XX".
//
// The extension only uses basic objects (HTTPService, Role, Language) that exist
// in all format versions, so downgrading is always safe for this extension.
// MUST be sorted by minMinor descending.
var platformFormatVersions = []struct {
	minMinor int    // minimum platform minor version (8.3.X)
	version  string // XML format version
}{
	{27, "2.20"},
	{26, "2.19"},
	{25, "2.18"},
	{24, "2.17"},
	{23, "2.16"},
	{22, "2.15"},
	{21, "2.14"},
	{20, "2.13"},
	{19, "2.12"},
	{18, "2.11"},
	{17, "2.10"},
	{16, "2.9.1"},
	{15, "2.9"},
	{14, "2.8"},
}

// platformOlderThan performs a tuple comparison of (major, minor) against
// (targetMajor, targetMinor). This correctly handles cross-major comparisons:
// e.g. platform 8.5.1 (major=5, minor=1) is NOT older than 8.3.14 (target 3, 14).
func platformOlderThan(major, minor, targetMajor, targetMinor int) bool {
	if major != targetMajor {
		return major < targetMajor
	}
	return minor < targetMinor
}

// parsePlatformVersion determines the platform major and minor version numbers
// (where major is the second number in 8.X and minor is the third number in
// 8.X.Y). If overrideVersion is provided (e.g. "8.3.13" or "8.3.14.1234"),
// it takes priority. Otherwise the version is extracted from the platform
// executable path. Returns (0, 0) when the version cannot be determined.
func parsePlatformVersion(platformExe, overrideVersion string) (major, minor int) {
	src := overrideVersion
	if src == "" {
		src = platformExe
	}
	maj, min, ok := extractPlatformMinor(src)
	if !ok {
		return 0, 0
	}
	return maj, min
}

// Install extracts embedded XML sources to a temp dir, patches the XML format
// version for compatibility with the detected platform, and loads it into 1C.
// If platformExe is empty, the platform is auto-detected.
// platformVersion is an optional override (e.g. "8.3.13") for cases when the
// version cannot be detected from the platform path automatically.
// When serverMode is true, the database is treated as a client-server infobase
// (MS SQL, PostgreSQL) and DESIGNER is invoked with /S instead of /F.
//
//garble:ignore
func Install(srcFS embed.FS, dbPath string, serverMode bool, platformExe, dbUser, dbPassword, platformVersion string) error {
	if platformExe == "" {
		var err error
		platformExe, err = FindPlatform()
		if err != nil {
			return fmt.Errorf("finding 1C platform: %w", err)
		}
	}
	fmt.Printf("Platform: %s\n", platformExe)

	// Extract extension XML sources to temp dir.
	extDir, err := os.MkdirTemp("", "mcp-1c-ext-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(extDir)

	if err := extractFS(srcFS, "src", extDir); err != nil {
		return fmt.Errorf("extracting extension sources: %w", err)
	}

	// Patch XML format version to match the target platform.
	fmtVer := formatVersionForPlatform(platformExe)
	if err := patchFormatVersion(extDir, fmtVer); err != nil {
		return fmt.Errorf("patching format version: %w", err)
	}

	// Pre-patch extension XML when the platform version is known. This avoids
	// unnecessary DESIGNER round-trips for platforms that would otherwise fail
	// on compat mode, unsupported elements, or inherited properties.
	cfgPath := filepath.Join(extDir, "Configuration.xml")
	major, minor := parsePlatformVersion(platformExe, platformVersion)

	if major > 0 { // version detected
		if platformOlderThan(major, minor, 3, 10) { // platform < 8.3.10
			return fmt.Errorf("платформа 8.%d.%d не поддерживается, минимальная версия 8.3.10", major, minor)
		}
		if platformOlderThan(major, minor, 3, 14) { // platform < 8.3.14
			// Patch compat mode down to 8.3.10
			if patchErr := patchExtensionXML(cfgPath, "Version8_3_10", ""); patchErr != nil {
				return fmt.Errorf("pre-patching extension compat mode: %w", patchErr)
			}
			// Strip unsupported elements (KeepMapping, InternalInfo, Role ClassId)
			if stripErr := stripUnsupportedElements(extDir); stripErr != nil {
				return fmt.Errorf("pre-patching unsupported elements: %w", stripErr)
			}
			// Strip inherited properties (DefaultRoles, DefaultRunMode, etc.)
			if stripErr := stripInheritedProperties(cfgPath); stripErr != nil {
				return fmt.Errorf("pre-patching inherited properties: %w", stripErr)
			}
			// Print info about role assignment
			fmt.Fprintln(os.Stderr, "Примечание: роль MCP_ОсновнаяРоль установлена с правами доступа к HTTP-сервису.")
			fmt.Fprintln(os.Stderr, "Пользователям с ролью \"Полные права\" дополнительных действий не требуется.")
			fmt.Fprintln(os.Stderr, "Для остальных пользователей назначьте роль MCP_ОсновнаяРоль вручную в Конфигураторе.")
		}
	}

	// Load extension XML into extension configuration.
	// We do NOT call /ManageCfgExtensions -delete upfront because that command
	// opens a GUI window on some platforms and hangs. Instead, we attempt to load
	// directly (optimistic path for fresh installs). If loading fails with
	// "Уже существует" (object already exists), we delete the old extension and retry.
	fmt.Println("Loading extension into database...")
	err = runDesigner(platformExe, dbPath, serverMode, dbUser, dbPassword,
		"/LoadConfigFromFiles", extDir,
		"-Extension", extensionName,
	)

	// If extension already exists, delete it and retry the load.
	if err != nil && strings.Contains(err.Error(), "Уже существует") {
		if delErr := runDesigner(platformExe, dbPath, serverMode, dbUser, dbPassword,
			"/ManageCfgExtensions", "-delete",
			"-Extension", extensionName,
		); delErr != nil {
			return fmt.Errorf("deleting old extension before retry: %w", delErr)
		}
		fmt.Println("Removed old extension:", extensionName)

		err = runDesigner(platformExe, dbPath, serverMode, dbUser, dbPassword,
			"/LoadConfigFromFiles", extDir,
			"-Extension", extensionName,
		)
	}

	if err != nil {
		// Platforms older than 8.3.15 do not recognize
		// KeepMappingToExtendedConfigurationObjectsByIDs, InternalInfo
		// sections, or certain ClassId values in extension XML files.
		// Strip these elements and retry.
		if strings.Contains(err.Error(), "KeepMappingToExtendedConfigurationObjectsByIDs") ||
			strings.Contains(err.Error(), "InternalInfo") ||
			strings.Contains(err.Error(), "идентификатор класса") {
			fmt.Println("Retrying without unsupported XML elements (old platform)...")
			if stripErr := stripUnsupportedElements(extDir); stripErr != nil {
				return fmt.Errorf("loading extension config: strip unsupported elements: %w", stripErr)
			}
			err = runDesigner(platformExe, dbPath, serverMode, dbUser, dbPassword,
				"/LoadConfigFromFiles", extDir,
				"-Extension", extensionName,
			)
		}

		// When the base configuration does not have DefaultRunMode set to
		// ManagedApplication, DESIGNER rejects the extension with a controlled
		// property mismatch error mentioning "ОсновнойРежимЗапуска".
		// Remove the property and retry.
		if err != nil && strings.Contains(err.Error(), "ОсновнойРежимЗапуска") {
			fmt.Println("Retrying without DefaultRunMode property (controlled property mismatch)...")
			cfgData, readErr := os.ReadFile(cfgPath)
			if readErr != nil {
				return fmt.Errorf("loading extension config: reading Configuration.xml: %w", readErr)
			}
			cfgData = defaultRunModeRe.ReplaceAll(cfgData, nil)
			if writeErr := os.WriteFile(cfgPath, cfgData, 0o644); writeErr != nil {
				return fmt.Errorf("loading extension config: writing Configuration.xml: %w", writeErr)
			}
			err = runDesigner(platformExe, dbPath, serverMode, dbUser, dbPassword,
				"/LoadConfigFromFiles", extDir,
				"-Extension", extensionName,
			)
		}

		// When the extension's compatibility mode is higher than the base
		// configuration's, DESIGNER rejects it with an error mentioning
		// "режим совместимости". First try Version8_3_10 (still supports roles),
		// and only fall back to DontUse as a last resort.
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "режим совместимости") {
			fmt.Println("Retrying with compatibility mode 8.3.10...")
			if patchErr := patchExtensionXML(cfgPath, "Version8_3_10", ""); patchErr != nil {
				return fmt.Errorf("loading extension config: patch compat mode: %w", patchErr)
			}
			err = runDesigner(platformExe, dbPath, serverMode, dbUser, dbPassword,
				"/LoadConfigFromFiles", extDir,
				"-Extension", extensionName,
			)

			// If Version8_3_10 still too high, fall back to DontUse.
			if err != nil && strings.Contains(strings.ToLower(err.Error()), "режим совместимости") {
				fmt.Println("Retrying without compatibility mode...")
				if patchErr := patchExtensionXML(cfgPath, "DontUse", ""); patchErr != nil {
					return fmt.Errorf("loading extension config: patch compat mode: %w", patchErr)
				}
				err = runDesigner(platformExe, dbPath, serverMode, dbUser, dbPassword,
					"/LoadConfigFromFiles", extDir,
					"-Extension", extensionName,
				)
			}
		}

		// Old configurations (compat mode 8.3.13 and below) reject extensions
		// that override inherited properties. Strip all controlled properties
		// from the extension Configuration.xml and retry.
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "переопределение свойств заимствованных объектов") {
			fmt.Println("Retrying without inherited properties (old compat mode)...")
			if patchErr := stripInheritedProperties(cfgPath); patchErr != nil {
				return fmt.Errorf("loading extension config: strip inherited properties: %w", patchErr)
			}
			err = runDesigner(platformExe, dbPath, serverMode, dbUser, dbPassword,
				"/LoadConfigFromFiles", extDir,
				"-Extension", extensionName,
			)
			if err == nil {
				fmt.Fprintln(os.Stderr, "Примечание: роль MCP_ОсновнаяРоль установлена с правами доступа к HTTP-сервису.")
				fmt.Fprintln(os.Stderr, "Пользователям с ролью \"Полные права\" дополнительных действий не требуется.")
				fmt.Fprintln(os.Stderr, "Для остальных пользователей назначьте роль MCP_ОсновнаяРоль вручную в Конфигураторе.")
			}
		}

		if err != nil {
			return fmt.Errorf("loading extension config: %w", err)
		}
	}

	// Apply extension to the database (separate call required).
	fmt.Println("Updating database...")
	if err := runDesigner(platformExe, dbPath, serverMode, dbUser, dbPassword,
		"/UpdateDBCfg",
		"-Extension", extensionName,
	); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "переопределение свойств заимствованных объектов") {
			fmt.Println("Retrying without inherited properties (old compat mode)...")
			if patchErr := stripInheritedProperties(cfgPath); patchErr != nil {
				return fmt.Errorf("updating database config: strip inherited properties: %w", patchErr)
			}
			if reloadErr := runDesigner(platformExe, dbPath, serverMode, dbUser, dbPassword,
				"/LoadConfigFromFiles", extDir,
				"-Extension", extensionName,
			); reloadErr != nil {
				return fmt.Errorf("reloading extension config after strip: %w", reloadErr)
			}
			if retryErr := runDesigner(platformExe, dbPath, serverMode, dbUser, dbPassword,
				"/UpdateDBCfg",
				"-Extension", extensionName,
			); retryErr != nil {
				return fmt.Errorf("updating database config: %w", retryErr)
			}
			fmt.Fprintln(os.Stderr, "Примечание: роль MCP_ОсновнаяРоль установлена с правами доступа к HTTP-сервису.")
			fmt.Fprintln(os.Stderr, "Пользователям с ролью \"Полные права\" дополнительных действий не требуется.")
			fmt.Fprintln(os.Stderr, "Для остальных пользователей назначьте роль MCP_ОсновнаяРоль вручную в Конфигураторе.")
			return nil
		}
		return fmt.Errorf("updating database config: %w", err)
	}
	return nil
}

// runDesigner executes 1C DESIGNER with given arguments, capturing output via /Out.
// When serverMode is true, uses /S (server connection string) instead of /F (file database).
func runDesigner(platformExe, dbPath string, serverMode bool, dbUser, dbPassword string, extraArgs ...string) error {
	logFile, err := os.CreateTemp("", "mcp-1c-log-*.txt")
	if err != nil {
		return fmt.Errorf("creating log file: %w", err)
	}
	logFile.Close()
	defer os.Remove(logFile.Name())

	args := buildDesignerArgs(dbPath, serverMode, dbUser, dbPassword, logFile.Name(), extraArgs...)

	cmd := exec.Command(platformExe, args...)
	cmd.CombinedOutput() //nolint:errcheck // exit code checked via log
	logData, _ := os.ReadFile(logFile.Name())
	logData = bytes.TrimLeft(logData, "\xef\xbb\xbf")

	// On older Windows platforms DESIGNER writes the log in Windows-1251
	// encoding. Detect non-UTF-8 content and convert it.
	if !utf8.Valid(logData) {
		if decoded, decErr := charmap.Windows1251.NewDecoder().Bytes(logData); decErr == nil {
			logData = decoded
		}
	}
	logStr := strings.TrimSpace(string(logData))

	if cmd.ProcessState == nil {
		return fmt.Errorf("1C DESIGNER failed to start: %s", platformExe)
	}
	if !cmd.ProcessState.Success() {
		if logStr != "" {
			return fmt.Errorf("1C DESIGNER failed (exit code %d):\n%s", cmd.ProcessState.ExitCode(), logStr)
		}
		return fmt.Errorf("1C DESIGNER failed with exit code %d (no log output)", cmd.ProcessState.ExitCode())
	}
	if logStr != "" {
		fmt.Println(logStr)
	}
	return nil
}

// buildDesignerArgs constructs the argument list for 1C DESIGNER.
// When serverMode is true, uses /S (server connection) instead of /F (file database).
func buildDesignerArgs(dbPath string, serverMode bool, dbUser, dbPassword, logPath string, extraArgs ...string) []string {
	connFlag := "/F"
	if serverMode {
		connFlag = "/S"
	}
	args := []string{"DESIGNER", connFlag, dbPath}
	if dbUser != "" {
		args = append(args, "/N", dbUser)
	}
	if dbPassword != "" {
		args = append(args, "/P", dbPassword)
	}
	args = append(args, "/WA-", "/DisableStartupDialogs", "/DisableStartupMessages")
	args = append(args, extraArgs...)
	args = append(args, "/Out", logPath)
	return args
}

// extractXMLTag extracts the text content of a simple XML tag like <TagName>value</TagName>.
func extractXMLTag(xml, tag string) string {
	re := regexp.MustCompile(`<` + tag + `>([^<]+)</` + tag + `>`)
	m := re.FindStringSubmatch(xml)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// replaceOrInsertXMLTag replaces an existing XML tag value or inserts a new tag before </Properties>.
func replaceOrInsertXMLTag(content, tagName, value string) string {
	re := regexp.MustCompile(`<` + tagName + `>[^<]+</` + tagName + `>`)
	replacement := "<" + tagName + ">" + value + "</" + tagName + ">"
	if re.MatchString(content) {
		return re.ReplaceAllString(content, replacement)
	}
	return strings.Replace(content, "</Properties>",
		"\t\t\t"+replacement+"\n\t\t</Properties>", 1)
}

// patchExtensionXML updates ConfigurationExtensionCompatibilityMode and InterfaceCompatibilityMode
// in the extension's Configuration.xml to match the target database.
func patchExtensionXML(path, compatMode, interfaceMode string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(data)

	if compatMode != "" {
		content = replaceOrInsertXMLTag(content, "ConfigurationExtensionCompatibilityMode", compatMode)
	}
	if interfaceMode != "" {
		content = replaceOrInsertXMLTag(content, "InterfaceCompatibilityMode", interfaceMode)
	}

	return os.WriteFile(path, []byte(content), 0o644)
}

// extractFS copies files from an embed.FS subtree into a directory on disk.
func extractFS(fsys embed.FS, root, destDir string) error {
	return fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		target := filepath.Join(destDir, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fsys.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// FindPlatform searches for the 1C platform executable on the current OS.
// Returns the last match from sorted glob results (latest version by lexical order).
func FindPlatform() (string, error) {
	patterns := platformPatterns()
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		if len(matches) > 0 {
			return matches[len(matches)-1], nil
		}
	}
	return "", fmt.Errorf("1C platform not found in standard paths")
}

// versionAttrRe matches the 1C XML dump format version attribute (version="2.X" or
// version="2.X.Y"). The "2." prefix naturally excludes the XML declaration
// (<?xml version="1.0"?>), so no separate guard is needed.
//
//garble:ignore
var versionAttrRe = regexp.MustCompile(`(version=")2\.\d+(?:\.\d+)?(")`)

// platformVersionRe extracts the 8.Major.Minor.Patch version from a platform path.
// Works with paths like:
//   - C:\Program Files\1cv8\8.3.27.1859\bin\1cv8.exe
//   - /opt/1cv8/x86_64/8.3.22.1709/1cv8
//   - /Applications/1cv8.localized/8.3.25.1000/1cv8.app/Contents/MacOS/1cv8
//
//garble:ignore
var platformVersionRe = regexp.MustCompile(`8\.(\d+)\.(\d+)`)

// extractPlatformMinor parses the platform path and returns the minor version number.
// For "8.3.27.1859" it returns (3, 27, true). For "8.5.1.100" it returns (5, 1, true).
// If the version cannot be parsed, it returns (0, 0, false).
func extractPlatformMinor(platformExe string) (major, minor int, ok bool) {
	m := platformVersionRe.FindStringSubmatch(platformExe)
	if len(m) < 3 {
		return 0, 0, false
	}
	maj, err1 := strconv.Atoi(m[1])
	min, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return maj, min, true
}

// formatVersionForPlatform determines the best XML format version for the given
// platform executable path. If the platform version cannot be detected, returns
// defaultFormatVersion (the safest baseline).
func formatVersionForPlatform(platformExe string) string {
	major, minor, ok := extractPlatformMinor(platformExe)
	if !ok {
		return defaultFormatVersion
	}

	// Platform 8.5+ uses format 2.21.
	if major >= 5 {
		return platform85FormatVersion
	}

	// Platform 8.3.X: find the highest format version it supports.
	if major == 3 {
		for _, pv := range platformFormatVersions {
			if minor >= pv.minMinor {
				return pv.version
			}
		}
	}

	return defaultFormatVersion
}

// patchFormatVersion walks the extension directory and rewrites the 1C XML dump
// format version attribute (version="2.X") in all XML files to match the target
// platform. This allows the same extension source to be loaded by older 1C
// platforms that do not recognize newer format versions.
func patchFormatVersion(dir, targetVersion string) error {
	replacement := []byte("${1}" + targetVersion + "${2}")
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".xml") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}

		patched := versionAttrRe.ReplaceAll(data, replacement)
		if bytes.Equal(patched, data) {
			return nil
		}

		return os.WriteFile(path, patched, 0o644)
	})
}

// keepMappingRe matches the KeepMappingToExtendedConfigurationObjectsByIDs
// element introduced in 1C 8.3.15. Older platforms reject this element.
//
//garble:ignore
var keepMappingRe = regexp.MustCompile(
	`\s*<KeepMappingToExtendedConfigurationObjectsByIDs>[^<]*</KeepMappingToExtendedConfigurationObjectsByIDs>`,
)

// internalInfoRe matches InternalInfo sections in extension XML files.
// These sections contain xr:ContainedObject/ClassId entries that older platforms
// (before 8.3.15) do not understand. The pattern handles both populated sections
// (with child elements) and empty self-closing tags.
//
//garble:ignore
var internalInfoRe = regexp.MustCompile(
	`(?s)\s*<InternalInfo\s*/>|\s*<InternalInfo>.*?</InternalInfo>`,
)

// roleContainedObjectRe matches xr:ContainedObject entries whose ClassId is
// fb282519-d103-4dd3-bc12-cb271d631dfc (Role). Platform 8.3.13 and below do
// not recognize this ClassId and reject the extension with
// "Неверный идентификатор класса хранимого объекта".
//
//garble:ignore
var roleContainedObjectRe = regexp.MustCompile(
	`(?s)\s*<xr:ContainedObject>\s*<xr:ClassId>fb282519-d103-4dd3-bc12-cb271d631dfc</xr:ClassId>\s*<xr:ObjectId>[^<]*</xr:ObjectId>\s*</xr:ContainedObject>`,
)

// defaultRunModeRe matches the DefaultRunMode element in extension Configuration.xml.
// Used to strip this property when the base configuration does not have it set to
// ManagedApplication (controlled property mismatch).
//
//garble:ignore
var defaultRunModeRe = regexp.MustCompile(`\s*<DefaultRunMode>[^<]*</DefaultRunMode>`)

// inheritedPropertyRe matches XML elements that may conflict with inherited base
// configuration properties in old compat modes (8.3.13 and below). Each element
// is matched including optional surrounding whitespace so the resulting XML stays
// well-formed. Elements may be single-line or span multiple lines.
//
//garble:ignore
var inheritedPropertyRe = regexp.MustCompile(
	`(?s)\s*<(?:` +
		`DefaultRunMode|UsePurposes|ScriptVariant|DefaultRoles|` +
		`Vendor|Version|DefaultLanguage|BriefInformation|DetailedInformation|` +
		`Copyright|VendorInformationAddress|ConfigurationInformationAddress` +
		`)>.*?</(?:` +
		`DefaultRunMode|UsePurposes|ScriptVariant|DefaultRoles|` +
		`Vendor|Version|DefaultLanguage|BriefInformation|DetailedInformation|` +
		`Copyright|VendorInformationAddress|ConfigurationInformationAddress` +
		`)>`,
)

// stripInheritedProperties removes XML elements from Configuration.xml that
// override inherited properties of the base configuration. This is needed for
// old configurations (compat mode 8.3.13 and below) that reject such overrides.
//
//garble:ignore
func stripInheritedProperties(cfgPath string) error {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	patched := inheritedPropertyRe.ReplaceAll(data, nil)
	return os.WriteFile(cfgPath, patched, 0o644)
}

// stripUnsupportedElements removes XML elements that older 1C platforms do not
// recognize: KeepMappingToExtendedConfigurationObjectsByIDs (from Configuration.xml),
// InternalInfo sections (from non-Configuration XML files in the extension
// directory), and Role ContainedObject entries from Configuration.xml's InternalInfo
// (ClassId fb282519... is not recognized by platforms 8.3.13 and below).
// Configuration.xml keeps the rest of its InternalInfo because it contains the
// extension UUID mapping required by the platform.
// These elements were introduced in 1C 8.3.15 and cause load failures on 8.3.10-8.3.14.
//
//garble:ignore
func stripUnsupportedElements(extDir string) error {
	// Remove KeepMappingToExtendedConfigurationObjectsByIDs and the Role
	// ContainedObject entry (ClassId fb282519...) from Configuration.xml.
	// The Role ClassId was introduced after 8.3.13 and causes
	// "Неверный идентификатор класса хранимого объекта" on older platforms.
	cfgPath := filepath.Join(extDir, "Configuration.xml")
	cfgData, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	cfgData = keepMappingRe.ReplaceAll(cfgData, nil)
	cfgData = roleContainedObjectRe.ReplaceAll(cfgData, nil)
	if err := os.WriteFile(cfgPath, cfgData, 0o644); err != nil {
		return err
	}

	// Remove InternalInfo sections from XML files in the extension directory,
	// except Configuration.xml which requires its InternalInfo section (contains
	// the extension UUID mapping).
	return filepath.WalkDir(extDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".xml") {
			return nil
		}
		// Configuration.xml needs its InternalInfo; skip it.
		if strings.EqualFold(filepath.Base(path), "configuration.xml") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		patched := internalInfoRe.ReplaceAll(data, nil)
		if bytes.Equal(patched, data) {
			return nil
		}
		return os.WriteFile(path, patched, 0o644)
	})
}

// platformPatterns returns glob patterns for finding 1C platform binary on the current OS.
func platformPatterns() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{
			`C:\Program Files\1cv8\8.*\bin\1cv8.exe`,
			`C:\Program Files (x86)\1cv8\8.*\bin\1cv8.exe`,
			`C:\Program Files\1cv8t\8.*\bin\1cv8t.exe`,
			`C:\Program Files (x86)\1cv8t\8.*\bin\1cv8t.exe`,
			`C:\Program Files\1cv82\8.*\bin\1cv8.exe`,
			`C:\Program Files (x86)\1cv82\8.*\bin\1cv8.exe`,
		}
	case "darwin":
		return []string{
			"/Applications/1cv8.localized/*/1cv8.app/Contents/MacOS/1cv8",
			"/Applications/1cv8t.localized/*/1cv8t.app/Contents/MacOS/1cv8t",
		}
	case "linux":
		return []string{
			"/opt/1cv8/x86_64/8.3.*/1cv8",
			"/opt/1cv8/x86_64/8.5.*/1cv8",
			"/opt/1C/v8.3/x86_64/1cv8",
		}
	default:
		return nil
	}
}
