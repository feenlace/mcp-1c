package dump

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func timeoutAfter() <-chan time.Time { return time.After(15 * time.Second) }

// ---------------------------------------------------------------------------
// safeJoin: BOTH .. traversal AND symlink escape must be rejected (guard #2).
// ---------------------------------------------------------------------------

func TestSafeJoin_RejectsDotDotEscape(t *testing.T) {
	root := t.TempDir()
	for _, segs := range [][]string{
		{"..", "etc", "passwd"},
		{"Subsystems", "..", "..", "outside"},
	} {
		if _, err := safeJoin(root, segs...); err == nil {
			t.Errorf("safeJoin(%v) = nil error, want a traversal rejection", segs)
		}
	}
}

func TestSafeJoin_AllowsInRootNonexistentPath(t *testing.T) {
	root := t.TempDir()
	// A path that does not exist yet is allowed (it is validated, not opened).
	if _, err := safeJoin(root, "Subsystems", "Продажи.xml"); err != nil {
		t.Errorf("safeJoin(in-root nonexistent) = %v, want nil", err)
	}
}

func TestSafeJoin_RejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	// root/link -> outside (a directory symlink escaping the dump).
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := safeJoin(root, "link", "secret.txt"); err == nil {
		t.Errorf("safeJoin through an escaping symlink = nil error, want rejection")
	}
}

func TestSafeJoin_AllowsInRootSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real", "x.xml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// root/link -> root/real (an in-dump symlink stays contained and is allowed).
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := safeJoin(root, "link", "x.xml"); err != nil {
		t.Errorf("safeJoin through an in-dump symlink = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// Traversal / symlink / cycle in the walk.
// ---------------------------------------------------------------------------

// A file-symlink under Subsystems/ that resolves OUTSIDE the dump must not be
// parsed (cross-dump disclosure) and must be NAMED in a warning.
func TestWalk_FileSymlinkEscape_Excluded(t *testing.T) {
	other := t.TempDir()
	secWrite(t, filepath.Join(other, "Subsystems", "Real.xml"), secSubBody("SecretFromOtherBase"))

	dumpA := t.TempDir()
	secWrite(t, filepath.Join(dumpA, "Subsystems", "Local.xml"), secSubBody("LocalOK"))
	// Evil.xml -> other/Subsystems/Real.xml (escapes dumpA).
	if err := os.Symlink(filepath.Join(other, "Subsystems", "Real.xml"),
		filepath.Join(dumpA, "Subsystems", "Evil.xml")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	subs, warnings, err := ParseAllSubsystemsCtx(context.Background(), dumpA)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if containsStr(flattenNames(subs), "SecretFromOtherBase") {
		t.Fatalf("cross-dump disclosure: escaping symlink was parsed; names=%v", flattenNames(subs))
	}
	if !warningsContain(warnings, "Evil") {
		t.Errorf("expected a warning naming the skipped escaping file; warnings=%v", warnings)
	}
}

// A directory-symlink for a child Subsystems/ that points at another dump must not
// fold that dump's subsystems in as children.
func TestWalk_DirSymlinkEscape_NoCrossDump(t *testing.T) {
	other := t.TempDir()
	secWrite(t, filepath.Join(other, "Subsystems", "Secret.xml"), secSubBody("SecretFromOtherBase"))

	dumpA := t.TempDir()
	secWrite(t, filepath.Join(dumpA, "Subsystems", "Root.xml"), secSubBody("Root"))
	if err := os.MkdirAll(filepath.Join(dumpA, "Subsystems", "Root"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Root/Subsystems -> other/Subsystems (escapes dumpA).
	if err := os.Symlink(filepath.Join(other, "Subsystems"),
		filepath.Join(dumpA, "Subsystems", "Root", "Subsystems")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	subs, _, err := ParseAllSubsystemsCtx(context.Background(), dumpA)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	names := flattenNames(subs)
	if !containsStr(names, "Root") {
		t.Fatalf("Root should be present; names=%v", names)
	}
	if containsStr(names, "SecretFromOtherBase") {
		t.Fatalf("cross-dump disclosure via dir-symlink; names=%v", names)
	}
}

// A symlink cycle must terminate (depth cap), not spin / overflow.
func TestWalk_SymlinkCycle_Terminates(t *testing.T) {
	old := maxSubsystemTreeDepth
	maxSubsystemTreeDepth = 5
	defer func() { maxSubsystemTreeDepth = old }()

	dumpA := t.TempDir()
	secWrite(t, filepath.Join(dumpA, "Subsystems", "A.xml"), secSubBody("A"))
	if err := os.MkdirAll(filepath.Join(dumpA, "Subsystems", "A"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A/Subsystems -> Subsystems (in-dump cycle: safeJoin passes, depth cap stops it).
	if err := os.Symlink(filepath.Join(dumpA, "Subsystems"),
		filepath.Join(dumpA, "Subsystems", "A", "Subsystems")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	done := make(chan struct{})
	var warnings []string
	go func() {
		_, warnings, _ = ParseAllSubsystemsCtx(context.Background(), dumpA)
		close(done)
	}()
	select {
	case <-done:
	case <-timeoutAfter():
		t.Fatal("walk did not terminate on a symlink cycle (depth cap missing)")
	}
	if !warningsContain(warnings, "глубин") {
		t.Errorf("expected a depth-exceeded warning; warnings=%v", warnings)
	}
}

// ---------------------------------------------------------------------------
// Malicious-dump DoS bounds.
// ---------------------------------------------------------------------------

// XML nested beyond the depth cap is rejected (bounded), not decoded into a stack
// overflow, and the dropped subsystem is named.
func TestWalk_DeepXML_Bounded(t *testing.T) {
	old := maxSubsystemXMLDepth
	maxSubsystemXMLDepth = 10
	defer func() { maxSubsystemXMLDepth = old }()

	dir := t.TempDir()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<MetaDataObject><Subsystem><Properties><Name>Deep</Name></Properties>")
	const levels = 40
	for i := 0; i < levels; i++ {
		b.WriteString("<ChildObjects><Subsystem><Properties><Name>x</Name></Properties>")
	}
	for i := 0; i < levels; i++ {
		b.WriteString("</Subsystem></ChildObjects>")
	}
	b.WriteString("</Subsystem></MetaDataObject>")
	secWrite(t, filepath.Join(dir, "Subsystems", "Deep", "Ext", "Subsystem.xml"), b.String())

	subs, warnings, err := ParseAllSubsystemsCtx(context.Background(), dir)
	if err != nil {
		t.Fatalf("err = %v, want nil (dropped, not fatal)", err)
	}
	if len(subs) != 0 {
		t.Errorf("deep-XML subsystem should be dropped; got %+v", subs)
	}
	if !warningsContain(warnings, "Deep") {
		t.Errorf("expected a warning naming the dropped Deep subsystem; warnings=%v", warnings)
	}
}

// A per-file byte cap drops an oversized subsystem file (no unbounded read).
func TestWalk_OversizedFile_Bounded(t *testing.T) {
	old := maxSubsystemFileBytes
	maxSubsystemFileBytes = 2048
	defer func() { maxSubsystemFileBytes = old }()

	dir := t.TempDir()
	items := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		items = append(items, fmt.Sprintf("Catalog.Объект%d", i))
	}
	secWrite(t, filepath.Join(dir, "Subsystems", "Huge.xml"), secSubBody("Huge", items...))
	secWrite(t, filepath.Join(dir, "Subsystems", "Small.xml"), secSubBody("Small"))

	subs, warnings, err := ParseAllSubsystemsCtx(context.Background(), dir)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	names := flattenNames(subs)
	if containsStr(names, "Huge") {
		t.Errorf("oversized Huge should be dropped; names=%v", names)
	}
	if !containsStr(names, "Small") {
		t.Errorf("Small should survive; names=%v", names)
	}
	if !warningsContain(warnings, "Huge") {
		t.Errorf("expected a warning naming Huge; warnings=%v", warnings)
	}
}

// Deep on-disk nesting is bounded by the tree depth cap.
func TestWalk_DeepOnDiskNesting_Bounded(t *testing.T) {
	old := maxSubsystemTreeDepth
	maxSubsystemTreeDepth = 3
	defer func() { maxSubsystemTreeDepth = old }()

	dir := t.TempDir()
	rel := "Subsystems"
	for i := 0; i < 8; i++ {
		secWrite(t, filepath.Join(dir, rel, "A.xml"), secSubBody("A"))
		rel = filepath.Join(rel, "A", "Subsystems")
	}

	done := make(chan struct{})
	var warnings []string
	go func() {
		_, warnings, _ = ParseAllSubsystemsCtx(context.Background(), dir)
		close(done)
	}()
	select {
	case <-done:
	case <-timeoutAfter():
		t.Fatal("deep on-disk walk did not terminate")
	}
	if !warningsContain(warnings, "глубин") {
		t.Errorf("expected a depth-exceeded warning; warnings=%v", warnings)
	}
}

// The node/breadth cap drops overflow subsystems and names one.
func TestWalk_NodeCap_Bounded(t *testing.T) {
	old := maxSubsystemNodes
	maxSubsystemNodes = 3
	defer func() { maxSubsystemNodes = old }()

	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("S%02d", i)
		secWrite(t, filepath.Join(dir, "Subsystems", name+".xml"), secSubBody(name))
	}
	subs, warnings, err := ParseAllSubsystemsCtx(context.Background(), dir)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(flattenNames(subs)) > maxSubsystemNodes {
		t.Errorf("node cap not enforced: got %d subsystems, cap %d", len(flattenNames(subs)), maxSubsystemNodes)
	}
	if !warningsContain(warnings, "превышено число подсистем") {
		t.Errorf("expected a node-cap warning; warnings=%v", warnings)
	}
}

// ---------------------------------------------------------------------------
// Path disclosure: an unreadable Subsystems/ must NOT leak the absolute path.
// ---------------------------------------------------------------------------

func TestWalk_UnreadableSubsystemsDir_PathFree(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses chmod perms")
	}
	dir := t.TempDir()
	subDir := filepath.Join(dir, "Subsystems")
	secWrite(t, filepath.Join(subDir, "X.xml"), secSubBody("X"))
	if err := os.Chmod(subDir, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(subDir, 0o755) }()

	_, _, err := ParseAllSubsystemsCtx(context.Background(), dir)
	if err == nil {
		t.Fatal("expected an error for an unreadable Subsystems/ dir")
	}
	if strings.Contains(err.Error(), dir) {
		t.Fatalf("error leaks the absolute dump path %q: %v", dir, err)
	}
	if strings.Contains(err.Error(), subDir) {
		t.Fatalf("error leaks the Subsystems path: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Silent-drop: a malformed subsystem must be NAMED in warnings, never lost.
// ---------------------------------------------------------------------------

func TestWalk_MalformedSubsystem_Named(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Subsystems", "Оплата.xml"), secSubBody("Оплата"))
	secWrite(t, filepath.Join(dir, "Subsystems", "Продажи.xml"),
		`<?xml version="1.0"?><MetaDataObject><Subsystem><Properties><Name>Продажи</Name>`)

	subs, warnings, err := ParseAllSubsystemsCtx(context.Background(), dir)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if containsStr(flattenNames(subs), "Продажи") {
		t.Errorf("malformed Продажи should be dropped from the tree")
	}
	if !warningsContain(warnings, "Продажи") {
		t.Fatalf("the dropped subsystem must be NAMED in warnings; warnings=%v", warnings)
	}
}

// In Ext layout a subsystem file that EXISTS but cannot be opened (permission
// denied) is a non-ENOENT failure and must be NAMED, not silently dropped. A
// genuinely missing Ext/Subsystem.xml (ENOENT) stays silent (a normal non-subsystem dir).
func TestWalkExt_UnreadableFile_Named(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses chmod perms")
	}
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Subsystems", "Good", "Ext", "Subsystem.xml"), secSubBody("Good"))
	badFile := filepath.Join(dir, "Subsystems", "Bad", "Ext", "Subsystem.xml")
	secWrite(t, badFile, secSubBody("Bad"))
	if err := os.Chmod(badFile, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(badFile, 0o644) }()

	subs, warnings, err := ParseAllSubsystemsCtx(context.Background(), dir)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	names := flattenNames(subs)
	if !containsStr(names, "Good") {
		t.Errorf("readable Good should survive; names=%v", names)
	}
	if containsStr(names, "Bad") {
		t.Errorf("unreadable Bad should be dropped; names=%v", names)
	}
	if !warningsContain(warnings, "Bad") {
		t.Fatalf("an unreadable Ext subsystem file must be NAMED, not silently dropped; warnings=%v", warnings)
	}
}

// A cancelled context aborts the walk.
func TestWalk_ContextCancelled_Aborts(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Subsystems", "A.xml"), secSubBody("A"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	_, _, err := ParseAllSubsystemsCtx(ctx, dir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// Crafted Content / Name strings are pure display data and drive no filesystem
// access (the only traversal channel is symlinks), so a hostile dump parses inert.
func TestWalk_HostileContentAndName_Inert(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Subsystems", "Ok.xml"),
		secSubBody("../../evil", "Catalog.../../../../etc/passwd"))

	subs, _, err := ParseAllSubsystemsCtx(context.Background(), dir)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(subs) != 1 {
		t.Fatalf("want 1 subsystem, got %+v", subs)
	}
}

// SF-2: layout detection must use the containment-checked join, so a Subsystems/
// that is a symlink escaping the dump is refused (default layout) instead of being
// followed to probe out-of-dump existence / metadata. Before the fix detection used
// filepath.Join + os.ReadDir/os.Stat, which followed the symlink and misreported
// layoutRoot from the outside directory's contents.
func TestDetectSubsystemLayout_EscapingSubsystemsSymlink_NoProbe(t *testing.T) {
	outside := t.TempDir()
	// The outside dir looks like a Hierarchical Subsystems/ (direct <Name>.xml), so
	// if detection followed the symlink it would misreport layoutRoot.
	secWrite(t, filepath.Join(outside, "OutsideSub.xml"), secSubBody("OutsideSub"))

	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "Subsystems")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	layout, err := detectSubsystemLayout(dir)
	if err != nil {
		t.Fatalf("detectSubsystemLayout err = %v, want nil", err)
	}
	if layout == layoutRoot {
		t.Fatalf("detection followed an escaping Subsystems symlink and probed outside (got layoutRoot)")
	}

	// End to end the walk must also yield an empty tree with no outside content.
	subs, _, werr := ParseAllSubsystemsCtx(context.Background(), dir)
	if werr != nil {
		t.Fatalf("walk err = %v, want nil", werr)
	}
	if containsStr(flattenNames(subs), "OutsideSub") {
		t.Fatalf("out-of-dump subsystem leaked via an escaping Subsystems symlink; names=%v", flattenNames(subs))
	}
}
