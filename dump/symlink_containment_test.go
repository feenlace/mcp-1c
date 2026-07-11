package dump

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGetContent_SymlinkContainment proves a malicious dump cannot exfiltrate an
// outside host file by planting a .bsl that is a symlink pointing out of the dump
// root. A pre-fix build followed the symlink in readModuleContent / loadBSLPaths
// and returned the outside file's bytes verbatim through GetContent and
// search_code. The fix (pathWithinRoot containment) must:
//   - refuse the escaping symlink (GetContent not-ok, marker never searchable),
//   - still serve an ordinary in-root module, and
//   - still serve an in-root symlink whose target stays under the dump root.
func TestGetContent_SymlinkContainment(t *testing.T) {
	const (
		outsideMarker  = "OUTSIDE_SECRET_MARKER_7f3a2b"
		controlMarker  = "INROOT_CONTROL_MARKER_9c1d4e"
		escapeID       = "ОбщийМодуль.Escape.Модуль"
		controlID      = "ОбщийМодуль.Control.Модуль"
		inRootLinkID   = "ОбщийМодуль.InRootLink.Модуль"
		controlRelPath = "CommonModules/Control/Ext/Module.bsl"
	)

	dumpRoot := t.TempDir()
	outsideDir := t.TempDir() // a sibling temp dir; its realpath is NOT under dumpRoot

	// An outside host file the attacker wants to steal (analogue of /etc/passwd).
	outsideSecret := filepath.Join(outsideDir, "secret.bsl")
	if err := os.WriteFile(outsideSecret, []byte("Процедура X()\n\t"+outsideMarker+"\nКонецПроцедуры\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A legitimate in-root module (control: must always be served).
	mkBSLFile(t, dumpRoot, controlRelPath,
		"Процедура Y()\n\t"+controlMarker+"\nКонецПроцедуры\n")

	// Malicious module: CommonModules/Escape/Ext/Module.bsl -> outside secret.
	escapeLink := filepath.Join(dumpRoot, filepath.FromSlash("CommonModules/Escape/Ext/Module.bsl"))
	if err := os.MkdirAll(filepath.Dir(escapeLink), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideSecret, escapeLink); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	// Benign in-root symlink: CommonModules/InRootLink/Ext/Module.bsl -> the
	// control module's real file (target stays under the dump root). Must still
	// be served, proving the fix does not over-block legitimate in-root symlinks.
	inRootLink := filepath.Join(dumpRoot, filepath.FromSlash("CommonModules/InRootLink/Ext/Module.bsl"))
	if err := os.MkdirAll(filepath.Dir(inRootLink), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dumpRoot, filepath.FromSlash(controlRelPath)), inRootLink); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	idx, err := NewIndex(dumpRoot, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

	// (1) The escaping symlink must NOT leak the outside file via GetContent.
	if got, ok := idx.GetContent(escapeID); ok && strings.Contains(got, outsideMarker) {
		t.Errorf("SECURITY: GetContent(%q) exfiltrated outside host file: %q", escapeID, got)
	}

	// (1b) Belt: no module at all may expose the outside marker via GetContent.
	for _, name := range idx.ModuleNames() {
		if got, ok := idx.GetContent(name); ok && strings.Contains(got, outsideMarker) {
			t.Errorf("SECURITY: GetContent(%q) exposed outside marker: %q", name, got)
		}
	}

	// (2) The escaping symlink must NOT leak the outside file via search_code.
	for _, mode := range []SearchMode{SearchModeSmart, SearchModeExact, SearchModeRegex} {
		matches, _, serr := idx.Search(SearchParams{Query: outsideMarker, Mode: mode, Limit: 500})
		if serr != nil {
			t.Fatalf("Search(mode=%s): %v", mode, serr)
		}
		for _, m := range matches {
			if strings.Contains(m.Context, outsideMarker) {
				t.Errorf("SECURITY: Search(mode=%s) leaked outside file in %q: %q", mode, m.Module, m.Context)
			}
		}
	}

	// (3) The ordinary in-root module must still be served.
	if got, ok := idx.GetContent(controlID); !ok || !strings.Contains(got, controlMarker) {
		t.Errorf("regression: in-root control module not served: ok=%v content=%q", ok, got)
	}

	// (4) The benign in-root symlink (target under root) must still be served.
	if got, ok := idx.GetContent(inRootLinkID); !ok || !strings.Contains(got, controlMarker) {
		t.Errorf("regression: in-root symlink not served: ok=%v content=%q", ok, got)
	}
}
