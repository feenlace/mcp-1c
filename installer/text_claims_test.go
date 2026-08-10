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

// disavowalFragments negate a claim from the FRONT while leaving every word of it
// in place. Russian does this naturally, and the contiguous-phrase defence does
// not survive it alone: a sentence can quote both phrases verbatim and still say
// the opposite. They are checked against every claim.
var disavowalFragments = []string{
	"неверно, что",
	"это не так",
	"не соответствует",
	"на самом деле наоборот",
	"вопреки замеру",
	"ничего подобного",
	"замер этого не показывает",
}

// claimHolds reports why the text fails to make the claim, or "" when it holds.
//
// The comparison is CASE FOLDED. It used to be case sensitive, and three
// inversions walked through it: a capitalised negating particle, a natural
// Russian sentence with the opposite meaning, and one that reused both
// contiguous phrases and disavowed the measurement outright. A guard that only
// sees lower case is a guard against typing mistakes, not against lies.
func claimHolds(text string, c textClaim) string {
	lower := strings.ToLower(text)
	for _, m := range c.must {
		if !strings.Contains(lower, strings.ToLower(m)) {
			return fmt.Sprintf("missing %q", m)
		}
	}
	forbidden := append(append([]string{}, c.mustNot...), disavowalFragments...)
	for _, m := range forbidden {
		if strings.Contains(lower, strings.ToLower(m)) {
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
			// States what is TRUE OF THE FILE, not what caused it: three paths
			// besides the flag remove the declaration, so naming the flag as the
			// cause would be false on all three.
			what:    "the loaded configuration does NOT declare the role, and there is no automatic access",
			text:    roleNoteStrippedLines[0],
			must:    []string{"в загруженной конфигурации", "объявления основной роли нет", "автоматического доступа"},
			mustNot: []string{"объявление основной роли есть", "доступ сохранён", "снято флагом"},
			negated: "Внимание: в загруженной конфигурации объявление основной роли есть, и автоматический доступ сохранён.",
		},
		{
			what:    "the causes are the flag AND the old platform / old compat mode paths",
			text:    roleNoteStrippedLines[1],
			must:    []string{"--strip-default-roles", "8.3.14", "режимах совместимости"},
			mustNot: []string{"только под флагом", "исключительно под флагом"},
			negated: "Так выходит только под флагом --strip-default-roles.",
		},
		{
			// The pairing is the claim, so the fragments are contiguous phrases
			// rather than single words: the negation of this sentence reuses
			// every word in it and swaps only which account keeps the service.
			what: "an ADMINISTRATOR account keeps the service, an ordinary restricted account LOSES it",
			text: roleNoteStrippedLines[2],
			must: []string{"администратора доступ сохраняет", "ограниченными правами теряет сервис"},
			mustNot: []string{"администратора доступ теряет", "администратор получает отказ",
				// The refuted claim. It was measured false on a real typical
				// configuration and must not come back.
				"Полные права"},
			negated: "Померено: учётная запись администратора доступ теряет, " +
				"а обычная учётная запись с ограниченными правами сервис сохраняет.",
		},
		{
			what:    "the account the CONNECTOR uses is the one to check, and to be given the role BY HAND",
			text:    roleNoteStrippedLines[3],
			must:    []string{"коннектор", "ограничены", "назначьте", "MCP_ОсновнаяРоль", "вручную", "Конфигураторе"},
			mustNot: []string{"назначается автоматически", "назначать не нужно"},
			negated: "Учётной записи коннектора роль MCP_ОсновнаяРоль назначается автоматически, " +
				"вручную ничего делать не нужно, даже если права ограничены.",
		},
		{
			what:    "the extension CANNOT do it for them, because 1С forbids it",
			text:    roleNoteStrippedLines[4],
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

				// The SAME checker, run over three inversions, must complain
				// about each. These are the mutations, performed here rather
				// than trusted, and all three were measured green before the
				// matcher was case folded and given the disavowal list.
				for _, bad := range []struct{ how, text string }{
					{"the negation", c.negated},
					{"the negation SHOUTED, same words, different case", strings.ToUpper(c.negated)},
					{"a leading disavowal quoting the sentence verbatim", "Неверно, что " + c.text},
				} {
					if why := claimHolds(bad.text, c); why == "" {
						t.Errorf("the checker accepts %s, which claims the OPPOSITE of %q, so it would "+
							"not notice the text being inverted.\ntext: %q", bad.how, c.what, bad.text)
					}
				}
			}
		})
	}
}

// TestNoTextClaimsFullRightsUsersAreRefused pins the one claim that was measured
// FALSE and shipped anyway.
//
// The note under --strip-default-roles used to tell the customer that users
// holding «Полные права» are refused along with everybody else. On two synthetic
// file bases that was what happened. On БухгалтерияПредприятияУчебная 3.0.111.25,
// nine real users, the element flipped five times, the administrator account
// answered 200 in every arm and noticed nothing, while an ordinary account
// holding 198 roles and no full rights went 200 to 403.
//
// A wrong sentence about who loses access sends the reader to check the wrong
// account, so it is worse than silence. This guard is deliberately blunt: no
// customer-facing text of this package may claim that a full-rights user is
// refused, in either note, in any wording that names the role.
func TestNoTextClaimsFullRightsUsersAreRefused(t *testing.T) {
	texts := map[string][]string{
		"roleNoteLines":         roleNoteLines,
		"roleNoteStrippedLines": roleNoteStrippedLines,
		"notAppliedNote(false)": strings.Split(notAppliedNote(false), "\n"),
		"notAppliedNote(true)":  strings.Split(notAppliedNote(true), "\n"),
	}
	// Both spellings the repository uses for the role.
	forbidden := []string{"Полные права", "ПолныеПрава"}

	scanned := 0
	for name, lines := range texts {
		if len(lines) == 0 {
			t.Errorf("%s is empty, so scanning it proves nothing", name)
		}
		for _, line := range lines {
			scanned++
			for _, f := range forbidden {
				if strings.Contains(line, f) {
					t.Errorf("%s names %q in customer-facing text. Measured on a real typical "+
						"configuration: an administrator account keeps the service when the "+
						"declaration is stripped. Any sentence built on that role is either the "+
						"refuted claim or an invitation to check the wrong account.\nline: %q",
						name, f, line)
				}
			}
		}
	}
	if scanned < 12 {
		t.Fatalf("only %d lines were scanned; the texts are longer than that and the scan is not "+
			"reaching them", scanned)
	}

	// Positive control: the same scan, over a line that DOES make the refuted
	// claim, must find it. Otherwise the zeros above are a scanner that matches
	// nothing.
	control := "Остальные получают отказ, включая пользователей с ролью \"Полные права\"."
	hit := false
	for _, f := range forbidden {
		if strings.Contains(control, f) {
			hit = true
		}
	}
	if !hit {
		t.Fatal("the scan cannot find the refuted claim in the sentence that made it, so its verdict " +
			"on the shipped texts means nothing")
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
