// Package nativecheck builds explicitly trusted selected external-object source.
// It never opens or executes the resulting report/processing in Enterprise mode.
package nativecheck

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/feenlace/mcp-1c/dump"
)

type ExternalCheckRequest struct {
	PlatformExecutable       string
	SourceRoot, ManifestPath string
	SourceTreeSHA256         string
	Kind                     string
	TargetFileDatabase       string
	OutputRoot, ArtifactPath string
	User, Password           string
}
type ExternalCheckResult struct {
	SourceTreeSHA256  string   `json:"source_tree_sha256,omitempty"`
	ArtifactSHA256    string   `json:"artifact_sha256,omitempty"`
	ArtifactPath      string   `json:"artifact_path,omitempty"`
	NativeCheckStatus string   `json:"native_check_status"`
	NativeReturnCode  *int     `json:"native_return_code,omitempty"`
	UIOpenStatus      string   `json:"ui_open_status"`
	Stage             string   `json:"stage"`
	ReasonCode        string   `json:"reason_code"`
	Warnings          []string `json:"warnings,omitempty"`
}

func checkFailure(r ExternalCheckResult, status, reason string) (ExternalCheckResult, error) {
	r.NativeCheckStatus = status
	r.ReasonCode = reason
	if status == "unavailable" {
		r.Warnings = []string{"Configure an installed Designer executable, an owned synthetic file database and a separate output directory; check platform license availability, then retry with the verified source digest and a new artifact path."}
	} else {
		r.Warnings = []string{"Inspect the selected source and reason_code; verify its complete digest and choose a new output path before retrying. The checker does not execute the object; source patch and backups remain available."}
	}
	if reason == "cancelled" {
		return r, errors.Join(context.Canceled, errors.New(reason))
	}
	if reason == "deadline_exceeded" {
		return r, errors.Join(context.DeadlineExceeded, errors.New(reason))
	}
	return r, errors.New(reason)
}

func checkRoot(path string) (*os.Root, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("explicit_absolute_directory_required")
	}
	st, err := os.Lstat(path)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("existing_nonsymlink_directory_required")
	}
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, errors.New("directory_unavailable")
	}
	return r, nil
}
func checkRelative(root *os.Root, path string) (os.FileInfo, error) {
	if !filepath.IsLocal(path) || filepath.ToSlash(filepath.Clean(path)) != path || strings.ContainsAny(path, "\\\x00\r\n") || path == "." {
		return nil, errors.New("confined_relative_path_required")
	}
	parts := strings.Split(path, "/")
	for i := range parts {
		st, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("symlinks_unsupported")
		}
		if i == len(parts)-1 {
			return st, nil
		}
		if !st.IsDir() {
			return nil, errors.New("parent_directory_required")
		}
	}
	return nil, errors.New("invalid_path")
}

// CheckExternal compiles an exact admitted snapshot with Designer's source-build command.
// Passed means an artifact was built, not full module syntax validation or GUI help-open.
func CheckExternal(ctx context.Context, input ExternalCheckRequest) (ExternalCheckResult, error) {
	result := ExternalCheckResult{NativeCheckStatus: "failed", UIOpenStatus: "NOT_RUN", Stage: "preflight"}
	if err := ctx.Err(); err != nil {
		return checkFailure(result, "failed", "cancelled")
	}
	if input.Kind != "ExternalReport" && input.Kind != "ExternalDataProcessor" {
		return checkFailure(result, "failed", "unsupported_kind")
	}
	source, err := dump.InspectExternalSource(ctx, input.SourceRoot, input.ManifestPath)
	if err != nil {
		return checkFailure(result, "failed", "source_unavailable_or_unsupported")
	}
	result.SourceTreeSHA256 = source.SourceTreeSHA256
	if input.SourceTreeSHA256 == "" || input.SourceTreeSHA256 != source.SourceTreeSHA256 || input.Kind != source.Kind {
		return checkFailure(result, "failed", "trusted_source_digest_mismatch")
	}
	platform, err := os.Stat(input.PlatformExecutable)
	if err != nil || !filepath.IsAbs(input.PlatformExecutable) || !platform.Mode().IsRegular() || platform.Mode().Perm()&0111 == 0 {
		return checkFailure(result, "unavailable", "installed_platform_unavailable")
	}
	target, err := checkRoot(input.TargetFileDatabase)
	if err != nil {
		return checkFailure(result, "unavailable", "explicit_owned_file_fixture_required")
	}
	defer target.Close()
	st, err := checkRelative(target, "1Cv8.1CD")
	if err != nil || !st.Mode().IsRegular() {
		return checkFailure(result, "unavailable", "file_fixture_unavailable")
	}
	output, err := checkRoot(input.OutputRoot)
	if err != nil {
		return checkFailure(result, "unavailable", "explicit_output_root_required")
	}
	defer output.Close()
	sourceAbs, err := filepath.Abs(input.SourceRoot)
	if err != nil {
		return checkFailure(result, "failed", "source_root_unavailable")
	}
	sourceAbs, err = filepath.EvalSymlinks(sourceAbs)
	if err != nil {
		return checkFailure(result, "failed", "source_root_unavailable")
	}
	outputAbs, err := filepath.EvalSymlinks(input.OutputRoot)
	if err != nil {
		return checkFailure(result, "failed", "output_root_unavailable")
	}
	rel, err := filepath.Rel(sourceAbs, outputAbs)
	if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return checkFailure(result, "failed", "output_must_be_outside_source_root")
	}
	if !filepath.IsLocal(input.ArtifactPath) || filepath.ToSlash(filepath.Clean(input.ArtifactPath)) != input.ArtifactPath || strings.ContainsAny(input.ArtifactPath, "\\\x00\r\n") {
		return checkFailure(result, "failed", "confined_artifact_path_required")
	}
	ext := ".epf"
	if input.Kind == "ExternalReport" {
		ext = ".erf"
	}
	if filepath.Ext(input.ArtifactPath) != ext {
		return checkFailure(result, "failed", "artifact_suffix_must_match_kind")
	}
	if parent := filepath.ToSlash(filepath.Dir(input.ArtifactPath)); parent != "." {
		st, e := checkRelative(output, parent)
		if e != nil || !st.IsDir() {
			return checkFailure(result, "failed", "artifact_parent_unavailable")
		}
	}
	artifactRel, e := filepath.Rel(sourceAbs, filepath.Join(outputAbs, filepath.FromSlash(input.ArtifactPath)))
	if e != nil || artifactRel == "." || (artifactRel != ".." && !strings.HasPrefix(artifactRel, ".."+string(filepath.Separator))) {
		return checkFailure(result, "failed", "artifact_must_be_outside_source_root")
	}
	if _, err := output.Lstat(input.ArtifactPath); !errors.Is(err, os.ErrNotExist) {
		return checkFailure(result, "failed", "artifact_collision")
	}
	var token [12]byte
	_, _ = rand.Read(token[:])
	session := ".nativecheck-" + hex.EncodeToString(token[:])
	if err := output.Mkdir(session, 0700); err != nil {
		return checkFailure(result, "failed", "private_session_unavailable")
	}
	defer output.RemoveAll(session)
	if err := output.Mkdir(session+"/source", 0700); err != nil {
		return checkFailure(result, "failed", "snapshot_unavailable")
	}
	inputRoot, err := checkRoot(sourceAbs)
	if err != nil {
		return checkFailure(result, "failed", "source_root_unavailable")
	}
	defer inputRoot.Close()
	result.Stage = "snapshot"
	for _, file := range source.Files {
		st, err := checkRelative(inputRoot, file.Path)
		if err != nil || !st.Mode().IsRegular() || st.Size() != file.Size {
			return checkFailure(result, "failed", "source_changed_before_snapshot")
		}
		f, err := inputRoot.OpenFile(file.Path, os.O_RDONLY|checkNonblock, 0)
		if err != nil {
			return checkFailure(result, "failed", "source_changed_before_snapshot")
		}
		opened, e := f.Stat()
		if e != nil || !opened.Mode().IsRegular() || !os.SameFile(st, opened) {
			f.Close()
			return checkFailure(result, "failed", "source_changed_before_snapshot")
		}
		data, e := io.ReadAll(io.LimitReader(f, 8<<20+1))
		f.Close()
		sum := sha256.Sum256(data)
		if e != nil || int64(len(data)) != file.Size || hex.EncodeToString(sum[:]) != file.SHA256 {
			return checkFailure(result, "failed", "source_changed_before_snapshot")
		}
		path := session + "/source/" + file.Path
		if err := output.MkdirAll(filepath.ToSlash(filepath.Dir(path)), 0700); err != nil {
			return checkFailure(result, "failed", "snapshot_directory_failed")
		}
		f, e = output.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return checkFailure(result, "failed", "snapshot_write_failed")
		}
		_, e = f.Write(data)
		closeErr := f.Close()
		if e != nil || closeErr != nil {
			return checkFailure(result, "failed", "snapshot_write_failed")
		}
		if err := ctx.Err(); err != nil {
			return checkFailure(result, "failed", "cancelled")
		}
	}
	buildCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	sessionAbs := filepath.Join(input.OutputRoot, session)
	builtRel := session + "/artifact" + ext
	args := []string{"DESIGNER", "/F", input.TargetFileDatabase, "/WA-", "/DisableStartupDialogs", "/DisableStartupMessages"}
	if input.User != "" {
		args = append(args, "/N", input.User)
	}
	if input.Password != "" {
		args = append(args, "/P", input.Password)
	}
	args = append(args, "/LoadExternalDataProcessorOrReportFromFiles", filepath.Join(sessionAbs, "source", filepath.FromSlash(input.ManifestPath)), filepath.Join(input.OutputRoot, filepath.FromSlash(builtRel)), "/Out", filepath.Join(sessionAbs, "native.log"))
	command := exec.CommandContext(buildCtx, input.PlatformExecutable, args...)
	privateOutput := &checkOutput{cancel: cancel}
	command.Stdout = privateOutput
	command.Stderr = privateOutput
	result.Stage = "external_build"
	if err := command.Start(); err != nil {
		return checkFailure(result, "unavailable", "native_start_unavailable")
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var runErr error
	overLimit := false
loop:
	for {
		select {
		case runErr = <-done:
			break loop
		case <-ticker.C:
			if st, e := output.Stat(session + "/native.log"); e == nil && st.Size() > 1<<20 {
				overLimit = true
				cancel()
			}
		}
	}
	if command.ProcessState != nil {
		rc := command.ProcessState.ExitCode()
		result.NativeReturnCode = &rc
	}
	if overLimit || privateOutput.exceeded {
		return checkFailure(result, "failed", "native_output_limit")
	}
	if buildCtx.Err() != nil {
		reason := "cancelled"
		if errors.Is(buildCtx.Err(), context.DeadlineExceeded) {
			reason = "deadline_exceeded"
		}
		return checkFailure(result, "failed", reason)
	}
	if logInfo, e := output.Stat(session + "/native.log"); e == nil && logInfo.Size() > 1<<20 {
		return checkFailure(result, "failed", "native_output_limit")
	}
	logData, logErr := output.ReadFile(session + "/native.log")
	if logErr == nil && len(logData) > 1<<20 {
		return checkFailure(result, "failed", "native_output_limit")
	}
	// Normal progress echoes caller paths: a project named "license-warning" is not a diagnostic.
	var diagnostics []string
	for _, line := range strings.Split(strings.ToLower(string(logData)+privateOutput.text()), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if strings.HasPrefix(line, "корневой файл выгрузки:") || strings.HasPrefix(line, "целевой каталог или файл:") || strings.HasPrefix(line, "root dump file:") || strings.HasPrefix(line, "target directory or file:") {
			continue
		}
		diagnostics = append(diagnostics, line)
	}
	privateText := strings.Join(diagnostics, "\n")
	if strings.Contains(privateText, "лицензи") || strings.Contains(privateText, "license") {
		return checkFailure(result, "unavailable", "native_license_unavailable")
	}
	if runErr != nil {
		return checkFailure(result, "failed", "native_build_failed")
	}
	if strings.Contains(privateText, "не является свойством") || strings.Contains(privateText, "неверное свойство") || strings.Contains(privateText, "не входит в состав") || strings.Contains(privateText, "invalid property") || strings.Contains(privateText, "предупреждени") || strings.Contains(privateText, "warning") || strings.Contains(privateText, "ошибка") || strings.Contains(privateText, "error:") {
		return checkFailure(result, "failed", "native_diagnostics_require_review")
	}
	artifact, err := output.Open(builtRel)
	if err != nil {
		return checkFailure(result, "failed", "native_artifact_missing")
	}
	info, err := artifact.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 128<<20 {
		artifact.Close()
		return checkFailure(result, "failed", "native_artifact_invalid")
	}
	hash := sha256.New()
	_, err = io.Copy(hash, io.LimitReader(artifact, 128<<20+1))
	artifact.Close()
	if err != nil {
		return checkFailure(result, "failed", "artifact_hash_failed")
	}
	if err := ctx.Err(); err != nil {
		return checkFailure(result, "failed", "cancelled")
	}
	if err := output.Link(builtRel, input.ArtifactPath); err != nil {
		return checkFailure(result, "failed", "artifact_publication_collision")
	}
	result.ArtifactPath = input.ArtifactPath
	result.ArtifactSHA256 = hex.EncodeToString(hash.Sum(nil))
	result.NativeCheckStatus = "passed"
	result.ReasonCode = "artifact_built"
	result.Warnings = []string{"build_only_not_full_module_syntax_or_gui_validation"}
	return result, nil
}

type checkOutput struct {
	mu       sync.Mutex
	data     bytes.Buffer
	exceeded bool
	cancel   context.CancelFunc
}

func (w *checkOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) > 1<<20-w.data.Len() {
		w.exceeded = true
		w.cancel()
		return 0, errors.New("private_output_limit")
	}
	return w.data.Write(p)
}
func (w *checkOutput) text() string { w.mu.Lock(); defer w.mu.Unlock(); return w.data.String() }
