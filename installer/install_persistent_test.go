package installer

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/installer/testdata/persistent"
)

func TestInstallDispatchesPersistentSourcesWithoutDeleteFallback(t *testing.T) {
	for _, stripRoles := range []bool{false, true} {
		t.Run(strconv.FormatBool(stripRoles), func(t *testing.T) {
			designer := newFakeDesigner(t, fakeModeExistsThenRunModeAlways)
			err := Install(persistent.Source, `server\synthetic`, true, fakePlatformExe(t), "", "", "8.3.13", stripRoles)
			if err == nil || !strings.Contains(err.Error(), "loading persistent extension in place") {
				t.Fatalf("persistent source reached the legacy installer: %v", err)
			}
			if got := strings.Join(callOps(fakeCalls(t, designer)), ","); got != "/LoadConfigFromFiles" {
				t.Fatalf("persistent dispatch operations = %s, want one load without delete or apply", got)
			}
		})
	}
}

func TestPersistentInstallNeverDeletesOrRewritesSources(t *testing.T) {
	for _, tc := range []struct {
		name, mode, ops, failure string
	}{
		{"success", fakeModeOK, "/LoadConfigFromFiles,/UpdateDBCfg", ""},
		{"existing extension load refused", fakeModeExistsThenRunModeAlways, "/LoadConfigFromFiles", "loading persistent extension in place"},
		{"load failed", fakeModeLoadFails, "/LoadConfigFromFiles", "loading persistent extension in place"},
		{"run mode mismatch", fakeModeRunModeMismatch, "/LoadConfigFromFiles,/UpdateDBCfg", "applied database state is not verified"},
		{"inherited property mismatch", fakeModeInheritedOverride, "/LoadConfigFromFiles,/UpdateDBCfg", "applied database state is not verified"},
		{"extension absent after load", fakeModeCompatNotFound, "/LoadConfigFromFiles,/UpdateDBCfg", "applied database state is not verified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			designer := newFakeDesigner(t, tc.mode)
			sources := t.TempDir()
			config := shippedConfiguration(t)
			if err := os.WriteFile(filepath.Join(sources, "Configuration.xml"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			err := installPersistentExtension(sources, `server\synthetic`, true, fakePlatformExe(t), "", "")
			if tc.failure == "" && err != nil || tc.failure != "" && (err == nil || !strings.Contains(err.Error(), tc.failure)) {
				t.Fatalf("unexpected install result: %v", err)
			}
			calls := fakeCalls(t, designer)
			if got := strings.Join(callOps(calls), ","); got != tc.ops {
				t.Fatalf("operations = %s, want %s", got, tc.ops)
			}
			for _, call := range calls {
				args := strings.Join(call.args, "\t")
				if !strings.Contains(args, "/S\tserver\\synthetic") || !strings.Contains(args, "-Extension\t"+extensionName) {
					t.Fatalf("operation changed database or extension: %v", call.args)
				}
				if strings.Contains(strings.ToLower(args), "safe") || strings.Contains(args, "-delete") {
					t.Fatalf("operation changed security or deleted an extension: %v", call.args)
				}
			}
			got, err := os.ReadFile(filepath.Join(sources, "Configuration.xml"))
			if err != nil || string(got) != config {
				t.Fatalf("installation rewrote source metadata: %v", err)
			}
		})
	}
}

func TestPersistentInstallCanRetryAfterFailedApply(t *testing.T) {
	designer := newFakeDesigner(t, fakeModeUnrelatedApplyFailure)
	sources := t.TempDir()
	if err := os.WriteFile(filepath.Join(sources, "Configuration.xml"), []byte(shippedConfiguration(t)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := installPersistentExtension(sources, "synthetic", false, fakePlatformExe(t), "", ""); err == nil {
		t.Fatal("failed apply reported success")
	}
	if err := os.WriteFile(filepath.Join(designer, fakeModeFile), []byte(fakeModeOK), 0600); err != nil {
		t.Fatal(err)
	}
	if err := installPersistentExtension(sources, "synthetic", false, fakePlatformExe(t), "", ""); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(callOps(fakeCalls(t, designer)), ","); got != "/LoadConfigFromFiles,/UpdateDBCfg,/LoadConfigFromFiles,/UpdateDBCfg" {
		t.Fatalf("retry operations = %s", got)
	}
}
