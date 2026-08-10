package installer

import (
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// What the sentences SAY, not that some string was printed.
//
// Every guard on these two texts proved delivery: the note is attached to the
// error, the role note is printed once per successful install. Delivery is not
// truth. Replacing all three role-note lines and all three note claims with
// their exact negations left the package green, and a lie is the only defect
// this text can have: it is prose, it has no behaviour of its own, and the whole
// reason both texts exist is that the installer was saying something false.
//
// So each sentence carries the fragments that make its claim, and the fragments
// whose presence would invert it. The two are not symmetric on purpose:
// «не установлена» CONTAINS «установлена», so requiring the positive word alone
// passes on the negation. The forbidden list is what catches that.
//
// Each claim also carries a negated rendering, and the SAME checker is run over
// it and required to complain. That is the mutation this file performs on
// itself: without it, a claim whose fragments happen to be unfalsifiable would
// look exactly like a claim that holds.
// ---------------------------------------------------------------------------

// textClaim is one customer-facing sentence and the claim it makes.
type textClaim struct {
	what string // the claim, in English, for the failure message
	text string // the shipped sentence, taken from production
	// must are fragments without which the sentence no longer makes the claim.
	must []string
	// mustNot are fragments whose presence inverts or negates the claim.
	mustNot []string
	// negated is the sentence rewritten to claim the opposite. The checker is
	// required to reject it.
	negated string
}

// claimHolds reports why the text fails to make the claim, or "" when it holds.
func claimHolds(text string, c textClaim) string {
	for _, m := range c.must {
		if !strings.Contains(text, m) {
			return fmt.Sprintf("missing %q", m)
		}
	}
	for _, m := range c.mustNot {
		if strings.Contains(text, m) {
			return fmt.Sprintf("contains %q, which inverts the claim", m)
		}
	}
	return ""
}

func roleNoteClaims() []textClaim {
	return []textClaim{
		{
			what: "the role IS installed and it carries rights to the HTTP service",
			text: roleNoteLines[0],
			must: []string{"MCP_ОсновнаяРоль", "установлена", "прав", "HTTP-сервис"},
			mustNot: []string{"не установлена", "не установлен", "без прав", "не даёт",
				"не создана", "не содержит"},
			negated: "Примечание: роль MCP_ОсновнаяРоль не установлена и прав доступа к HTTP-сервису не даёт.",
		},
		{
			what:    "a user holding Полные права has to do NOTHING further",
			text:    roleNoteLines[1],
			must:    []string{"Полные права", "не требуется"},
			mustNot: []string{"действия требуются", "также назначьте", "тоже нужно"},
			negated: "Пользователям с ролью \"Полные права\" дополнительные действия требуются.",
		},
		{
			what:    "everybody else must be given the role BY HAND, in the Конфигуратор",
			text:    roleNoteLines[2],
			must:    []string{"MCP_ОсновнаяРоль", "назначьте", "вручную", "Конфигураторе"},
			mustNot: []string{"не назначайте", "назначается автоматически", "назначать не нужно"},
			negated: "Для остальных пользователей роль MCP_ОсновнаяРоль назначается автоматически.",
		},
	}
}

func notAppliedClaims() []textClaim {
	return []textClaim{
		{
			what:    "the database was NOT changed and the extension is loaded but NOT applied",
			text:    notAppliedHead,
			must:    []string{"не внесены", "загружено", "не применено"},
			mustNot: []string{"успешно", "изменения внесены", "и применено"},
			negated: "Изменения в базу данных внесены: расширение загружено в конфигурацию и применено.",
		},
		{
			what:    "an extension that was already there KEEPS working and the new one is stranded",
			text:    notAppliedPreviousKeepsWorking,
			must:    []string{"уже стояло", "продолжает работать", "прежняя"},
			mustNot: []string{"не работает", "перестала", "остановлена"},
			negated: "Если расширение в этой базе уже стояло, прежняя его версия больше не работает.",
		},
		{
			what:    "on a first install the extension does NOT work",
			text:    notAppliedFirstInstall,
			must:    []string{"первая установка", "не работает"},
			mustNot: []string{"уже работает", "работает штатно"},
			negated: "Если это первая установка, расширение уже работает.",
		},
		{
			what:    "the previous version was REMOVED, so nothing is serving now",
			text:    notAppliedPreviousDeleted,
			must:    []string{"удалена", "не работает"},
			mustNot: []string{"не удалена", "сохранена", "продолжает работать"},
			negated: "Прежняя версия расширения сохранена и продолжает работать.",
		},
		{
			what:    "the customer should fix the cause and INSTALL AGAIN",
			text:    notAppliedAdvice,
			must:    []string{"повторите установку"},
			mustNot: []string{"повторять установку не нужно", "не повторяйте"},
			negated: "Повторять установку не нужно.",
		},
	}
}

func TestCustomerFacingTextMakesTheClaimsItIsThereToMake(t *testing.T) {
	groups := []struct {
		name   string
		claims []textClaim
	}{
		{"role note", roleNoteClaims()},
		{"apply-failure note", notAppliedClaims()},
	}

	for _, group := range groups {
		t.Run(group.name, func(t *testing.T) {
			if len(group.claims) < 3 {
				t.Fatalf("%s has %d claims pinned; an emptied table is a green that proves nothing",
					group.name, len(group.claims))
			}
			for _, c := range group.claims {
				if len(c.must) == 0 || len(c.mustNot) == 0 || c.negated == "" {
					t.Errorf("the claim %q is not fully specified, so it cannot be falsified", c.what)
					continue
				}

				// The shipped sentence makes the claim.
				if why := claimHolds(c.text, c); why != "" {
					t.Errorf("the shipped text no longer claims that %s: %s\ntext: %q",
						c.what, why, c.text)
				}

				// The SAME checker, run over the negation, must complain. This is
				// the mutation, performed here rather than trusted.
				if why := claimHolds(c.negated, c); why == "" {
					t.Errorf("the checker accepts a sentence that claims the OPPOSITE of %q, so it "+
						"would not notice the text being inverted.\nnegated: %q", c.what, c.negated)
				}
			}
		})
	}
}

// TestEveryCustomerFacingSentenceIsPinned makes the tables above cover the whole
// texts. A sentence added to either text without a claim here is unpinned, and
// unpinned is exactly the state both texts were in.
func TestEveryCustomerFacingSentenceIsPinned(t *testing.T) {
	pinned := map[string]bool{}
	for _, c := range append(roleNoteClaims(), notAppliedClaims()...) {
		pinned[c.text] = true
	}

	for _, line := range roleNoteLines {
		if !pinned[line] {
			t.Errorf("this role-note line makes no pinned claim, so it could be replaced by its "+
				"negation without reddening anything: %q", line)
		}
	}
	// Both renderings, so the delete-path sentence is covered as well as the
	// two conditionals it replaces.
	for _, rendered := range []string{notAppliedNote(false), notAppliedNote(true)} {
		for _, line := range strings.Split(rendered, "\n") {
			if !pinned[line] {
				t.Errorf("this note sentence makes no pinned claim: %q", line)
			}
		}
	}

	// Control: the map really is doing work. A sentence that is NOT part of
	// either text must not be reported as pinned.
	if pinned["Расширение установлено успешно."] {
		t.Fatal("the pinned set answers yes to a sentence neither text contains, so its verdicts above " +
			"mean nothing")
	}
}
