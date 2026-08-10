package installer

import (
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// What the sentences SAY, not that some string was printed.
//
// Every guard on these texts proved delivery: the note is attached to the
// error, the role note is printed once per successful install. Delivery is not
// truth. Replacing every line of both texts with its exact negation left the
// package green, and a lie is the only defect this text can have: it is prose,
// it has no behaviour of its own, and the whole reason these texts exist is that
// the installer was saying something false.
//
// Three texts are covered: the role note as installed by default, the role note
// under --strip-default-roles, and the apply-failure note. The first two make
// OPPOSITE promises about who reaches the service, so swapping them would be a
// lie on both paths at once.
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
			what:    "the role IS installed and IS declared a default role of the extension",
			text:    roleNoteLines[0],
			must:    []string{"MCP_ОсновнаяРоль", "установлена", "объявлена основной ролью"},
			mustNot: []string{"не установлена", "не объявлена", "не создана"},
			negated: "Примечание: роль MCP_ОсновнаяРоль не установлена и не объявлена основной ролью расширения.",
		},
		{
			what:    "users who already hold roles of the configuration reach the service with NO further action",
			text:    roleNoteLines[1],
			must:    []string{"уже есть роли", "получают доступ", "без дополнительных действий"},
			mustNot: []string{"не получают доступ", "требуются дополнительные", "получают отказ"},
			negated: "Пользователи, у которых уже есть роли конфигурации, получают отказ и требуются дополнительные действия.",
		},
		{
			what:    "a user with NO roles is refused and must be given the role BY HAND",
			text:    roleNoteLines[2],
			must:    []string{"нет ни одной роли", "отказом", "назначьте", "вручную", "Конфигураторе"},
			mustNot: []string{"назначается автоматически", "назначать не нужно", "отвечает без отказа"},
			negated: "Пользователю, у которого нет ни одной роли, роль MCP_ОсновнаяРоль назначается автоматически.",
		},
	}
}

func roleNoteStrippedClaims() []textClaim {
	return []textClaim{
		{
			what:    "the declaration was REMOVED by the flag, and the automatic grant with it",
			text:    roleNoteStrippedLines[0],
			must:    []string{"снято флагом", "--strip-default-roles", "автоматический доступ"},
			mustNot: []string{"сохранено", "не снято", "доступ сохранён"},
			negated: "Примечание: объявление основной роли сохранено, автоматический доступ не снят.",
		},
		{
			what:    "ONLY users given the role explicitly are served, Полные права included in the refusal",
			text:    roleNoteStrippedLines[1],
			must:    []string{"только тем пользователям", "назначена явно", "получают отказ", "Полные права"},
			mustNot: []string{"всем пользователям", "получают доступ, включая", "отказ не"},
			negated: "Сервис отвечает всем пользователям, включая тех, кому роль MCP_ОсновнаяРоль не назначена.",
		},
		{
			what:    "the administrator must assign the role BY HAND to everyone using MCP",
			text:    roleNoteStrippedLines[2],
			must:    []string{"Назначьте", "MCP_ОсновнаяРоль", "вручную", "Конфигураторе", "каждому"},
			mustNot: []string{"назначается автоматически", "назначать не нужно"},
			negated: "Роль MCP_ОсновнаяРоль назначается каждому автоматически, вручную в Конфигураторе делать ничего не нужно.",
		},
		{
			what:    "the extension CANNOT do it for them, because 1С forbids it",
			text:    roleNoteStrippedLines[3],
			must:    []string{"не может", "не разрешает", "администрировать пользователей"},
			mustNot: []string{"расширение назначит", "сделает это за вас"},
			negated: "Расширение сделает это за вас: 1С разрешает коду расширения администрировать пользователей информационной базы.",
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
		{"role note, default", roleNoteClaims()},
		{"role note, --strip-default-roles", roleNoteStrippedClaims()},
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
	all := append(roleNoteClaims(), roleNoteStrippedClaims()...)
	for _, c := range append(all, notAppliedClaims()...) {
		pinned[c.text] = true
	}

	for _, set := range [][]string{roleNoteLines, roleNoteStrippedLines} {
		for _, line := range set {
			if !pinned[line] {
				t.Errorf("this role-note line makes no pinned claim, so it could be replaced by its "+
					"negation without reddening anything: %q", line)
			}
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
