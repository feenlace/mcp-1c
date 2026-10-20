package nativecheck

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/feenlace/mcp-1c/dump"
)

func checkFixture(t *testing.T, script string) ExternalCheckRequest {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("owned POSIX fake process test; native Windows command not claimed")
	}
	source := t.TempDir()
	data, err := os.ReadFile(filepath.Join("..", "dump", "testdata", "help-native", "HelpProbeProcessor.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "HelpProbeProcessor.xml"), data, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := dump.InspectExternalSource(context.Background(), source, "HelpProbeProcessor.xml")
	if err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	_ = os.WriteFile(filepath.Join(fixture, "1Cv8.1CD"), []byte("owned fake file fixture; not native acceptance"), 0600)
	platform := filepath.Join(t.TempDir(), "owned-fake-platform")
	if err := os.WriteFile(platform, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	return ExternalCheckRequest{PlatformExecutable: platform, SourceRoot: source, ManifestPath: info.ManifestPath, SourceTreeSHA256: info.SourceTreeSHA256, Kind: info.Kind, TargetFileDatabase: fixture, OutputRoot: t.TempDir(), ArtifactPath: "owned.epf"}
}

const fakeBuild = `while [ "$#" -gt 0 ]; do
 if [ "$1" = "/Execute" ]; then exit 9; fi
 if [ "$1" = "/LoadExternalDataProcessorOrReportFromFiles" ]; then
  shift; test -f "$1" || exit 3; shift; printf 'OWNED-ARTIFACT' > "$1" || exit 4
 fi
 shift
done
exit 0
`

func TestCheckExternalSnapshotBuildAndCollision(t *testing.T) {
	input := checkFixture(t, fakeBuild)
	r, err := CheckExternal(context.Background(), input)
	if err != nil || r.NativeCheckStatus != "passed" || r.UIOpenStatus != "NOT_RUN" || r.ReasonCode != "artifact_built" || r.ArtifactSHA256 == "" {
		t.Fatalf("mock build: %+v %v", r, err)
	}
	if b, e := os.ReadFile(filepath.Join(input.OutputRoot, input.ArtifactPath)); e != nil || string(b) != "OWNED-ARTIFACT" {
		t.Fatal("artifact not published")
	}
	if _, err := CheckExternal(context.Background(), input); err == nil {
		t.Fatal("collision accepted")
	}
	entries, _ := os.ReadDir(input.OutputRoot)
	if len(entries) != 1 {
		t.Fatalf("private session leaked: %v", entries)
	}
}
func TestCheckExternalRefusalsPrivacyAndCancellation(t *testing.T) {
	input := checkFixture(t, "printf 'license unavailable SENTINEL_HOST_PASSWORD' >&2\nexit 1\n")
	r, err := CheckExternal(context.Background(), input)
	data, _ := json.Marshal(r)
	if err == nil || r.NativeCheckStatus != "unavailable" || r.ReasonCode != "native_license_unavailable" || strings.Contains(string(data)+err.Error(), "SENTINEL") {
		t.Fatalf("privacy: %+v %v", r, err)
	}
	input.SourceTreeSHA256 = strings.Repeat("0", 64)
	r, err = CheckExternal(context.Background(), input)
	if err == nil || r.ReasonCode != "trusted_source_digest_mismatch" {
		t.Fatal("digest drift accepted")
	}
	input = checkFixture(t, "exec sleep 20\n")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	start := time.Now()
	r, err = CheckExternal(ctx, input)
	if err == nil || r.UIOpenStatus != "NOT_RUN" || time.Since(start) > 3*time.Second {
		t.Fatalf("cancellation: %+v %v", r, err)
	}
	input.PlatformExecutable = "/missing/installed/platform"
	r, err = CheckExternal(context.Background(), input)
	if err == nil || r.NativeCheckStatus != "unavailable" {
		t.Fatal("missing platform accepted")
	}
}

func TestCheckExternalRC0PropertyWarningIsNotPassed(t *testing.T) {
	input := checkFixture(t, strings.Replace(fakeBuild, "exit 0", "printf 'Неверное свойство объекта метаданных. Свойство X не входит в состав объекта метаданных ExternalDataProcessor.'\nexit 0", 1))
	r, err := CheckExternal(context.Background(), input)
	if err == nil || r.NativeCheckStatus != "failed" || r.ReasonCode != "native_diagnostics_require_review" || r.NativeReturnCode == nil || *r.NativeReturnCode != 0 {
		t.Fatalf("RC0 warning accepted: %+v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(input.OutputRoot, input.ArtifactPath)); !os.IsNotExist(err) {
		t.Fatal("warning artifact was published")
	}
}

func TestCheckExternalProgressPathIsNotDiagnostic(t *testing.T) {
	input := checkFixture(t, strings.Replace(fakeBuild, "exit 0", "printf 'Целевой каталог или файл: /owned/license-warning-error.epf\\n'\nexit 0", 1))
	r, err := CheckExternal(context.Background(), input)
	if err != nil || r.NativeCheckStatus != "passed" {
		t.Fatalf("path misclassified: %+v %v", r, err)
	}
}
