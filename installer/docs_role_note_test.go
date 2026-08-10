package installer

import (
	"os"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The role instruction has to be in the documentation, because on the manual
// route there is no stdout to print it to.
//
// docs/1c-setup.md walks a customer through downloading MCP_HTTPService.cfe and
// adding it in the Конфигуратор. That customer never runs the binary and never
// sees a line the installer prints. Everything printRoleNote says is exactly as
// true for them, and until now nothing in the tree made sure they were told.
//
// The guard is about coverage of the ROUTES, not about wording. Each file is cut
// at the heading that starts the manual route and both halves are required to
// carry the instruction, so adding it once at the top and leaving the manual
// route bare does not pass. The wording is not compared with roleNoteLines: the
// documentation is prose for a reader with a mouse and the note is a line in a
// terminal, and forcing them to be the same sentence would be pinning a
// coincidence. What is compared is the three FACTS the note carries.
// ---------------------------------------------------------------------------

// roleDocFact is one thing the reader has to be told, and a fragment that says
// it. Only fragments free of markup and of dashes are used, so the check does
// not depend on how the sentence is decorated.
type roleDocFact struct {
	what     string
	fragment string
}

var roleDocFacts = []roleDocFact{
	{"which role is installed", "MCP_ОсновнаяРоль"},
	{"who needs no further action", "Полные права"},
	{"the others must be given the role by hand", "вручную"},
}

// roleDocs are the documents that walk a reader through installing the
// extension, with the heading at which each one turns from the command line
// route to the manual one.
var roleDocs = []struct {
	path         string
	manualRoute  string
	manualRouteN string
}{
	{"../docs/1c-setup.md", "### Установка через Конфигуратор", "Установка через Конфигуратор"},
	{"../docs/getting-started.md", "**Вручную (если автоматическая не сработала):**", "Вручную"},
}

func TestRoleInstructionIsDocumentedOnBothInstallRoutes(t *testing.T) {
	if len(roleDocFacts) < 3 {
		t.Fatalf("the guard checks %d facts; the note carries three and all three matter",
			len(roleDocFacts))
	}

	for _, doc := range roleDocs {
		t.Run(doc.path, func(t *testing.T) {
			raw, err := os.ReadFile(doc.path)
			if err != nil {
				t.Fatalf("read %s: %v", doc.path, err)
			}
			text := string(raw)

			cut := strings.Index(text, doc.manualRoute)
			if cut < 0 {
				t.Fatalf("%s no longer contains the heading %q that starts its manual route, so this "+
					"test cannot tell the two routes apart", doc.path, doc.manualRoute)
			}
			automatic, manual := text[:cut], text[cut:]

			// Positive control: the cut produced two real halves, and each half
			// is the one it is claimed to be.
			if !strings.Contains(automatic, "--install") {
				t.Fatalf("%s: the half before %q does not mention --install, so it is not the command "+
					"line route and the split is wrong", doc.path, doc.manualRouteN)
			}
			if !strings.Contains(manual, "Конфигуратор") {
				t.Fatalf("%s: the half after %q does not mention the Конфигуратор, so it is not the "+
					"manual route and the split is wrong", doc.path, doc.manualRouteN)
			}

			for _, half := range []struct {
				name string
				text string
			}{
				{"the command line route", automatic},
				{"the manual route", manual},
			} {
				for _, fact := range roleDocFacts {
					if !strings.Contains(half.text, fact.fragment) {
						t.Errorf("%s, %s: nothing states %s (looked for %q). A reader who takes this "+
							"route is never told, and on the manual route there is no stdout to tell them",
							doc.path, half.name, fact.what, fact.fragment)
					}
				}
			}
		})
	}
}

// TestRoleInstructionSplitCanFail is the mutation the test above cannot perform
// on itself: it runs the same reading over a document that carries the
// instruction on one route only, and requires a complaint about the other.
func TestRoleInstructionSplitCanFail(t *testing.T) {
	const oneSidedDoc = "Ставится командой mcp-1c --install, роль MCP_ОсновнаяРоль назначается вручную, " +
		"кроме роли Полные права.\n" +
		"### Установка через Конфигуратор\nОткройте базу в Конфигураторе и нажмите F7.\n"

	cut := strings.Index(oneSidedDoc, "### Установка через Конфигуратор")
	if cut < 0 {
		t.Fatal("the control document lost its own heading")
	}
	automatic, manual := oneSidedDoc[:cut], oneSidedDoc[cut:]

	missing := 0
	for _, fact := range roleDocFacts {
		if !strings.Contains(manual, fact.fragment) {
			missing++
		}
	}
	if missing != len(roleDocFacts) {
		t.Errorf("the manual half of the one-sided control is missing %d of %d facts, want all of "+
			"them; the reading does not distinguish the halves", missing, len(roleDocFacts))
	}
	for _, fact := range roleDocFacts {
		if !strings.Contains(automatic, fact.fragment) {
			t.Errorf("the automatic half of the control should carry every fact, but %q is absent",
				fact.fragment)
		}
	}
}
