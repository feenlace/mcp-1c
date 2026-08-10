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
//
// DECLARED BOUNDARY, so that nobody reads this file as a proof of meaning.
// The defence against negation is a BLACKLIST: a fixed set of forbidden
// fragments per claim, plus disavowalFragments. A blacklist of the ways Russian
// can negate a sentence cannot be completed, and pretending otherwise would be
// worse than admitting it. Measured against this file as it stands, on the
// administrator pairing: of nine disavowal openings tried that are not on the
// list, nine got through, and of four inversions tried that keep every pinned
// phrase intact, four got through. Those two numbers describe the samples, not
// the guard. What they show is the shape: the blacklist stops exactly what it
// enumerates and nothing else.
//
// So what this file proves is bounded and worth stating plainly. It catches the
// inversions it enumerates, which are the ones that were actually shipped or
// actually attempted: the swap, the swap in a different case, and a leading
// disavowal. It does NOT prove that a sentence means what it claims. Widening
// the blacklist raises the cost of the next inversion; it never closes the set.
// The remaining defence is that a human wrote the sentence and another human
// read it.
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
			what:    "RIGHT NOW users who already hold roles of the configuration reach the service with no further action",
			text:    roleNoteLines[1],
			must:    []string{"уже есть роли", "получают доступ", "без дополнительных действий"},
			mustNot: []string{"не получают доступ", "требуются дополнительные", "получают отказ"},
			negated: "Сейчас пользователи, у которых уже есть роли конфигурации, получают отказ и требуются дополнительные действия.",
		},
		{
			// The claim that replaced «назначьте роль вручную в Конфигураторе».
			what:    "with Управление доступом, access goes through a профиль групп доступа, because a recalculation undoes both other mechanisms",
			text:    roleNoteLines[2],
			must:    []string{"Управление доступом", "через профиль групп доступа", "пересчёт ролей", "стирает", "выключает свойство"},
			mustNot: []string{"назначьте", "вручную в Конфигураторе", "сохраняется"},
			negated: "Если в конфигурации есть подсистема Управление доступом, назначьте роль вручную в Конфигураторе: пересчёт ролей пользователей ничего не меняет.",
		},
		{
			what:    "a recalculation is an ORDINARY administrative event, so direct assignment cannot be relied on there",
			text:    roleNoteLines[3],
			must:    []string{"обычное административное действие", "полагаться нельзя"},
			mustNot: []string{"редкое", "никогда не происходит", "можно полагаться"},
			negated: "Пересчёт ролей это редкое событие, поэтому на прямое назначение там можно полагаться.",
		},
		{
			what:    "WITHOUT Управление доступом, a direct assignment works and STAYS",
			text:    roleNoteLines[4],
			must:    []string{"нет", "напрямую", "сохраняется"},
			mustNot: []string{"не сохраняется", "стирается", "тоже стирается"},
			negated: "Если подсистемы Управление доступом в конфигурации нет, прямое назначение роли всё равно не сохраняется.",
		},
		{
			what:    "the SYMPTOM of the first case, given instead of a detection recipe we cannot ground",
			text:    roleNoteLines[5],
			must:    []string{"доступ работал и перестал", "роль у пользователя пропала"},
			mustNot: []string{"роль остаётся на месте", "доступ не меняется"},
			negated: "Признак того, что вы в первом случае: доступ не меняется и роль остаётся на месте.",
		},
		{
			what:    "the extension CANNOT do it for them: safe mode, and the platform refuses",
			text:    roleNoteCannotDoIt,
			must:    []string{"не может", "безопасном режиме", "отвергает"},
			mustNot: []string{"расширение назначит", "сделает это за вас"},
			negated: "Расширение сделает это за вас: оно работает вне безопасного режима, и платформа разрешает администрирование пользователей.",
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
			what: "the CONNECTOR's account must be granted access explicitly, and by which route depends on Управление доступом",
			text: roleNoteStrippedLines[3],
			must: []string{"коннектор", "выдать явно", "Управление доступом", "через профиль групп доступа",
				"иначе прямым назначением"},
			mustNot: []string{"назначается автоматически", "выдавать не нужно", "вручную в Конфигураторе"},
			negated: "Учётной записи коннектора доступ назначается автоматически, выдавать его не нужно.",
		},
		{
			what: "with Управление доступом a direct assignment does NOT survive, and the symptom says so",
			text: roleNoteStrippedLines[4],
			must: []string{"не держится", "пересчёт ролей", "стирает", "доступ работал и перестал"},
			// «не держится» CONTAINS «держится», so the forbidden fragment has to
			// be the positive form with its subject attached. This is the same
			// containment trap as «не установлена» against «установлена».
			mustNot: []string{"назначение держится", "сохраняется", "пересчёт ничего не меняет"},
			negated: "В конфигурации с Управление доступом прямое назначение держится и сохраняется, пересчёт ничего не меняет.",
		},
		{
			what:    "the extension CANNOT do it for them: safe mode, and the platform refuses",
			text:    roleNoteCannotDoIt,
			must:    []string{"не может", "безопасном режиме", "отвергает"},
			mustNot: []string{"расширение назначит", "сделает это за вас"},
			negated: "Расширение сделает это за вас: оно работает вне безопасного режима, и платформа разрешает администрирование пользователей.",
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

// TestNoTextTellsTheCustomerToAssignTheRoleByHand pins the second claim that was
// measured FALSE and shipped anyway, and this one shipped UNCONDITIONALLY.
//
// «назначьте роль MCP_ОсновнаяРоль вручную в Конфигураторе» printed on every
// successful install. On a configuration carrying the Управление доступом
// subsystem it is false: measured on a twin of a real Бухгалтерия 3.0.111.25,
// one call to УправлениеДоступомСлужебный.ОбновитьРолиПользователей() deleted
// exactly that role from the user and switched off the extension property that
// carries the automatic access, and the user went from 200 to 403.
//
// An instruction that stops working later is worse than no instruction: the
// customer follows it, sees it work, and discovers months later that access
// vanished. So the imperative is forbidden here, and both notes are required to
// carry BOTH branches of the split, because a note that mentions only one of them
// is the same defect wearing a condition.
func TestNoTextTellsTheCustomerToAssignTheRoleByHand(t *testing.T) {
	// The retired instruction, in the shapes it was actually shipped in.
	retired := []string{"вручную в конфигураторе", "назначьте ему роль", "назначьте ей роль"}

	notes := map[string][]string{
		"roleNoteLines":         roleNoteLines,
		"roleNoteStrippedLines": roleNoteStrippedLines,
	}

	scanned := 0
	for name, lines := range notes {
		joined := strings.ToLower(strings.Join(lines, "\n"))
		for _, line := range lines {
			scanned++
			for _, r := range retired {
				if strings.Contains(strings.ToLower(line), r) {
					t.Errorf("%s tells the customer to assign the role by hand (%q). Measured: on a "+
						"configuration with Управление доступом a recalculation of user roles deletes "+
						"exactly that assignment and switches off the property carrying the automatic "+
						"access.\nline: %q", name, r, line)
				}
			}
		}
		// Both branches of the split have to be present, or the note is right
		// about one kind of base and silent about the other.
		for _, required := range []string{"управление доступом", "профиль групп доступа"} {
			if !strings.Contains(joined, required) {
				t.Errorf("%s never mentions %q, so it does not tell the reader which of the two "+
					"configurations they are in or how access is delivered there", name, required)
			}
		}
	}
	if scanned < 10 {
		t.Fatalf("only %d note lines were scanned; both notes are longer than that together", scanned)
	}

	// Positive control: the scan finds the retired instruction in the sentence
	// that made it, so the zeros above are absence and not a scanner that
	// matches nothing.
	control := "Пользователю, у которого нет ни одной роли, сервис отвечает отказом: назначьте ему роль " +
		"MCP_ОсновнаяРоль вручную в Конфигураторе."
	hits := 0
	for _, r := range retired {
		if strings.Contains(strings.ToLower(control), r) {
			hits++
		}
	}
	if hits < 2 {
		t.Fatalf("the scan found %d of the retired shapes in the sentence that shipped them, so its "+
			"verdict on the shipped notes means nothing", hits)
	}
}

// TestEveryCustomerFacingSentenceHasAClaim proves COVERAGE and nothing more, and
// its old name promised more than that.
//
// textClaim.text is a REFERENCE to the production variable, not a copy of it, so
// editing a shipped sentence edits the claim's text with it and this test cannot
// see the change. Calling it "IsPinned" told the next reader that the text was
// nailed down here; it is not. What is nailed down here is that no sentence
// ships WITHOUT a claim entry, so adding a line to either text without adding a
// claim reddens. Edits to an existing sentence are defended one test up, by the
// must and mustNot fragments, within the boundary declared at the top of this
// file.
//
// The copy was deliberately not duplicated: a second spelling of every sentence
// is a second place to update on each edit, and it would catch only what the
// fragment tables already judge on meaning rather than on bytes.
func TestEveryCustomerFacingSentenceHasAClaim(t *testing.T) {
	claimed := map[string]bool{}
	all := append(roleNoteClaims(), roleNoteStrippedClaims()...)
	for _, c := range append(all, notAppliedClaims()...) {
		claimed[c.text] = true
	}

	for _, set := range [][]string{roleNoteLines, roleNoteStrippedLines} {
		for _, line := range set {
			if !claimed[line] {
				t.Errorf("this role-note line has no claim entry, so nothing above judges what it says: %q", line)
			}
		}
	}
	// Both renderings, so the delete-path sentence is covered as well as the
	// two conditionals it replaces.
	for _, rendered := range []string{notAppliedNote(false), notAppliedNote(true)} {
		for _, line := range strings.Split(rendered, "\n") {
			if !claimed[line] {
				t.Errorf("this note sentence has no claim entry, so nothing above judges what it says: %q", line)
			}
		}
	}

	// Control: the map really is doing work. A sentence that is NOT part of
	// either text must not be reported as pinned.
	if claimed["Расширение установлено успешно."] {
		t.Fatal("the claimed set answers yes to a sentence neither text contains, so its verdicts " +
			"above mean nothing")
	}
}
