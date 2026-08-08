//go:build unix

package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ===========================================================================
// THE SHORTFALL NOTE MAY NOT NAME A CAUSE THE SEARCH DID NOT ESTABLISH.
//
// dump.SearchStats.Unreadable is written in one place, searchSmart, and it counts
// the hits whose content dump.Index.GetContent refused. GetContent answers false
// in five places; the first, an index that is not ready, is unreachable here
// because SearchWithStats refuses before it picks a mode. That leaves FOUR, and
// the search learns which one only in the sense that it learns none of them: it
// receives `false` and nothing else.
//
//	!hasPath              the key has no file recorded behind it
//	!pathWithinRoot       the real path does not resolve, or resolves OUTSIDE the
//	                      dump root. Both a deleted file and a file that is THERE
//	                      and READABLE and reached through a symlink leaving the
//	                      dump root land here; the second is a deliberate security
//	                      refusal, planted-dump containment, and not a fault
//	refusedAsUnreadable   the per-generation negative set already holds the key,
//	                      whatever the file is doing now, until a reload drops it
//	a read error          including EMFILE / ENFILE, which dump/index.go's own
//	                      readFailureSaysSomethingAboutTheFile documents as facts
//	                      about the process and the machine rather than the file
//
// The shipped sentence «файлы изменились или удалены уже после того, как построен
// индекс» names two causes. The second is true of one arm of the second refusal.
// The FIRST IS TRUE OF NONE OF THEM: a file whose (mtime, size) stamp has moved is
// not refused at all, it is re-read and served, so a merely changed file cannot
// produce this note in the first place.
//
// WHAT THE CUSTOMER IS TOLD TODAY when the containment check fires: that their
// file was deleted, when the truth is that this product declined to follow a link
// out of the dump root. The fixture below plants exactly that and checks the whole
// note against a file it has just read off the disk itself.
// ===========================================================================

// TestTheShortfallNoteNamesNoCauseTheSearchDidNotEstablish drives a real
// search_code over a real index in which one module's file is present, readable,
// and refused by the dump-root containment policy.
//
// THE ASSERTION IS THE WHOLE LINE AND NOT A FORBIDDEN WORD. A denylist of causes
// passes the moment the next round writes a cause it does not list, and once the
// false clause is gone, asserting its absence is a check with no producer left.
// Pinning the note outright fails for ANY cause, including one nobody has written
// yet.
func TestTheShortfallNoteNamesNoCauseTheSearchDidNotEstablish(t *testing.T) {
	dir := mkSearchDump(t, 3)
	index := openSearchIndex(t, dir)

	// CONTROL 1: the intact dump answers in full, so every difference below is the
	// containment refusal's doing and not the fixture's.
	before := runSearch(t, index, "Процедура", 50)
	if got := searchRenderedMatches(before); got != 3 {
		t.Fatalf("control failed: an intact dump renders %d of 3 modules, so nothing below "+
			"is measured.\n%s", got, before)
	}
	if strings.Contains(before, "не показано") {
		t.Fatalf("control failed: a complete answer already carries a shortfall note.\n%s", before)
	}

	// THE ESCAPING SYMLINK IS PLANTED AFTER THE BUILD, because loadBSLPaths applies
	// the same containment rule and a link planted before it would simply never be
	// indexed. This is the state the check exists for: a dump rewritten under a live
	// server.
	victim := filepath.Join(dir, "CommonModules", "Модуль00", "Ext", "Module.bsl")
	outside := t.TempDir()
	target := filepath.Join(outside, "Module.bsl")
	if err := os.WriteFile(target, []byte("Процедура Тест00()\n    Сообщить(\"00\");\nКонецПроцедуры\n"), 0o644); err != nil {
		t.Fatalf("writing the outside file: %v", err)
	}
	if err := os.Remove(victim); err != nil {
		t.Fatalf("removing the in-root file: %v", err)
	}
	if err := os.Symlink(target, victim); err != nil {
		t.Skipf("this platform will not create a symlink (%v), so the containment refusal "+
			"cannot be produced here", err)
	}

	// CONTROL 2: the file IS there and IS readable, proved by reading it through the
	// link from this process. Without this the note below could be describing a file
	// that really is gone, which is the one case the shipped wording gets right.
	if _, err := os.ReadFile(victim); err != nil {
		t.Fatalf("control failed: the planted symlink is not readable (%v), so the answer "+
			"below would be about a file that really is unavailable", err)
	}

	text := runSearch(t, index, "Процедура", 50)

	// CONTROL 3: the refusal really happened and it is counted as Unreadable, taken
	// from the rendered answer's own numbers rather than assumed from the fixture.
	shown := searchRenderedMatches(text)
	if shown != 2 {
		t.Fatalf("control failed: %d of 3 modules were rendered, so the containment check "+
			"did not refuse exactly one and the note is about something else.\n%s", shown, text)
	}
	if got := searchHeaderTotal(t, text); got != 3 {
		t.Fatalf("control failed: the header counts %d, so the index no longer holds the "+
			"refused module and there is no shortfall to explain.\n%s", got, text)
	}

	// THE NOTE, PINNED WHOLE. It may state what happened (the content was not
	// obtained), what that does to the header, and the one action that clears the
	// state on all four refusals. It may not name a cause nothing established.
	want := fmt.Sprintf("> Показано %d из %d модулей. Ещё %d отобрано, но не показано: "+
		"получить содержимое этих модулей не удалось. Число в заголовке взято из индекса "+
		"и их всё ещё учитывает. Выполните выгрузку конфигурации заново и вызовите "+
		"reload_dump.", 2, 3, 1)
	assertShortfallLineIsExactly(t, text, "> Показано ", want)

	// AND NO OTHER LINE OF THE ANSWER NAMES A CAUSE EITHER. Pinning one line leaves
	// the sentence free to be reintroduced one line down, where every check above
	// still passes.
	assertNoLineClaimsTheFileChangedOrWentAway(t, text)
}

// assertShortfallLineIsExactly pins the whole line that starts with prefix, and
// fails when there is more than one such line: a second copy of the footer is a
// state no assertion here would otherwise see.
func assertShortfallLineIsExactly(t *testing.T, answer, prefix, want string) {
	t.Helper()
	var found []string
	for _, line := range strings.Split(answer, "\n") {
		if strings.HasPrefix(line, prefix) {
			found = append(found, line)
		}
	}
	if len(found) == 0 {
		t.Fatalf("control failed: no line starting %q in the answer.\n%s", prefix, answer)
	}
	if len(found) > 1 {
		t.Fatalf("the answer carries %d lines starting %q, so pinning one of them leaves the "+
			"others unchecked.\n%s", len(found), prefix, answer)
	}
	if found[0] != want {
		t.Errorf("the shortfall note is not the sentence this state licenses.\n got: %q\nwant: %q"+
			"\n\nfull answer:\n%s", found[0], want, answer)
	}
}

// assertNoLineClaimsTheFileChangedOrWentAway is the ADJACENT-LINE half. It is a
// denylist and is sound as one only because it is a second check beside a whole
// line pinned above: this one cannot certify the note, it can only catch the
// forbidden claim escaping into a neighbour.
func assertNoLineClaimsTheFileChangedOrWentAway(t *testing.T, answer string) {
	t.Helper()
	forbidden := []string{"изменились", "удалены", "удалён", "удален", "не удалось перечитать"}
	for _, line := range strings.Split(answer, "\n") {
		if !strings.HasPrefix(line, "> ") {
			continue // product notes are blockquotes; the body is the customer's code
		}
		for _, f := range forbidden {
			if strings.Contains(line, f) {
				t.Errorf("a note claims the file changed or went away (%q), over a module this "+
					"test has just read off the disk through its own link.\n%s", f, line)
			}
		}
	}
}
