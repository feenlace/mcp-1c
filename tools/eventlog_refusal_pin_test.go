package tools

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
)

// ---------------------------------------------------------------------------
// ONE HANDLER, SEVERAL 403s, AND NOTHING TOLD THEM APART.
//
// ЖурналРегистрацииPOST answers 403 at four sites in three classes. The first is
// the rights gate at the top: ПравоДоступа is false, the account may not read the
// journal at all. The second is further down: the user filter could not be
// resolved. The other two are the event filter's, and they fire when the base's
// own list of event names could not be obtained. Every one but the first is
// reachable ONLY AFTER the gate has passed, i.e. only by a caller who HAS the
// right. The module says so itself, and calls the rights cause «единственная
// заведомо исключённая» there.
//
// The renderer keyed remedyEventLogNoRight on the STATUS alone, so the second
// refusal was answered with «Это отказ по правам учётной записи, а не по отбору»
// and «Повторять вызов без отбора бесполезно». Both are false on that path: the
// account has the right, the filter is the only thing that leads there, and
// dropping the filter is precisely what avoids it.
//
// THIS FILE IS THE PIN BETWEEN THE TWO SIDES. The refusals are not typed in
// here: they are read out of the shipped module and driven through the real
// renderer. A change to either side alone therefore fails.
//
//   - change the status of a refusal in the module and the class the renderer
//     keys on is no longer reached, so the remedy disappears from the answer;
//   - change the diagnostic in the module and the prefix predicate stops
//     matching, with the same effect;
//   - change the predicate or the remedy in Go and the module's own text stops
//     being answered correctly.
//
// A test that spelled the strings out on both sides would agree with itself
// after either side moved.
// ---------------------------------------------------------------------------

// bslRefusal is one ОтветОшибка(<code>, "<text>"…) site in the module.
type bslRefusal struct {
	code int
	text string
	line int
}

// refusalSiteRE matches an answer built with a literal first fragment. The
// module splits long diagnostics across lines with "+", so only the FIRST
// fragment is captured; every predicate this file pins is a prefix predicate,
// which is exactly what a first fragment can decide.
var refusalSiteRE = regexp.MustCompile(`ОтветОшибка\((\d+),\s*"([^"]*)"`)

// eventLogRefusals reads every literal refusal out of ЖурналРегистрацииPOST.
func eventLogRefusals(t *testing.T) []bslRefusal {
	t.Helper()
	src, err := os.ReadFile(bslModule)
	if err != nil {
		t.Fatalf("reading the shipped extension module: %v", err)
	}
	lines := strings.Split(string(src), "\n")
	start, end := -1, -1
	for i, l := range lines {
		switch {
		case strings.HasPrefix(strings.TrimSpace(l), "Функция ЖурналРегистрацииPOST"):
			start = i
		case start >= 0 && end < 0 && strings.TrimSpace(l) == "КонецФункции":
			end = i
		}
	}
	if start < 0 || end < 0 {
		t.Fatal("ЖурналРегистрацииPOST was not found in the shipped module; its refusals " +
			"cannot be read and nothing below measures anything")
	}

	var out []bslRefusal
	for i := start; i <= end; i++ {
		m := refusalSiteRE.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		code, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("%s:%d: refusal status %q is not a number", bslModule, i+1, m[1])
		}
		out = append(out, bslRefusal{code: code, text: m[2], line: i + 1})
	}
	return out
}

// TestEventLogRefusalsAreAnsweredByTheirOwnCause drives every refusal the
// shipped module can answer with through the real renderer.
//
// Each one must get the advice written for IT. A refusal that lands on the other
// one's advice is the defect; a refusal that lands on neither is a new class
// nobody classified, and that fails too rather than passing by default.
func TestEventLogRefusalsAreAnsweredByTheirOwnCause(t *testing.T) {
	refusals := eventLogRefusals(t)

	// CONTROL: the walk read the function. Without this every loop below is
	// satisfied by an empty slice.
	if len(refusals) < 10 {
		t.Fatalf("the walk found %d literal refusals in ЖурналРегистрацииPOST; it used to find "+
			"ten, so the extractor is broken and every check below passes on nothing",
			len(refusals))
	}

	const (
		rightsMarker      = "Это отказ по правам учётной записи, а не по отбору"
		filterMarker      = "поиск пользователя для отбора не состоялся"
		eventFilterMarker = "не смогло получить у базы список имён событий"
	)

	rights, filter, eventFilter := 0, 0, 0
	for _, r := range refusals {
		if r.code != eventLogRefusalStatus {
			continue
		}
		text := renderFailure(headingEventLog, &onec.StatusError{
			StatusCode: r.code, BodyKind: onec.BodyKindExtension, Detail: r.text,
		})
		switch {
		case isEventLogRightsRefusal(r.text):
			rights++
			if !strings.Contains(text, rightsMarker) {
				t.Errorf("%s:%d answers %d %q, which this repository classifies as the rights "+
					"refusal, but the answer does not carry the rights remedy:\n%s",
					bslModule, r.line, r.code, r.text, text)
			}
			if strings.Contains(text, filterMarker) {
				t.Errorf("%s:%d: the rights refusal is answered with the user filter remedy:\n%s",
					bslModule, r.line, text)
			}
		case isEventLogUserFilterRefusal(r.text):
			filter++
			if !strings.Contains(text, filterMarker) {
				t.Errorf("%s:%d answers %d %q, the user filter refusal, but the answer does not "+
					"carry the remedy written for it:\n%s", bslModule, r.line, r.code, r.text, text)
			}
			// THE DEFECT. Only a caller who HAS the right reaches this line, so
			// naming the right as the cause names the one cause the gate above
			// has already excluded.
			if strings.Contains(text, rightsMarker) {
				t.Errorf("%s:%d is reachable only after the rights gate has passed, so the caller "+
					"HAS the right, and the answer still says it is a rights refusal:\n%s",
					bslModule, r.line, text)
			}
			if strings.Contains(text, "без отбора бесполезно") {
				t.Errorf("%s:%d fires only when a user filter was given, and the answer tells the "+
					"caller that retrying without the filter is pointless:\n%s",
					bslModule, r.line, text)
			}
		case isEventLogEventFilterRefusal(r.text):
			eventFilter++
			if !strings.Contains(text, eventFilterMarker) {
				t.Errorf("%s:%d answers %d %q, the event filter refusal, but the answer does not "+
					"carry the remedy written for it:\n%s", bslModule, r.line, r.code, r.text, text)
			}
			// Same defect as above, one filter along: this line too stands after
			// the rights gate, so naming the right names the one cause that is
			// already excluded.
			if strings.Contains(text, rightsMarker) {
				t.Errorf("%s:%d is reachable only after the rights gate has passed, so the caller "+
					"HAS the right, and the answer still says it is a rights refusal:\n%s",
					bslModule, r.line, text)
			}
			if strings.Contains(text, filterMarker) {
				t.Errorf("%s:%d is about the list of EVENT names and is answered with the user "+
					"filter remedy:\n%s", bslModule, r.line, text)
			}
		default:
			t.Errorf("%s:%d answers %d %q, and this repository classifies it as neither the "+
				"rights refusal, the user filter refusal nor the event filter refusal. It will be "+
				"answered with no cause "+
				"at all, which may be the wrong advice.", bslModule, r.line, r.code, r.text)
		}
	}

	// Every class must actually occur. A module that lost one of them would
	// satisfy every check above by never entering its branch.
	if rights != 1 {
		t.Errorf("the module has %d rights refusals at status %d, want exactly one; "+
			"tools/toolerror.go keys remedyEventLogNoRight on that status and that prefix, "+
			"so a refusal that moved off either of them is answered by nothing",
			rights, eventLogRefusalStatus)
	}
	if filter != 1 {
		t.Errorf("the module has %d user filter refusals at status %d, want exactly one",
			filter, eventLogRefusalStatus)
	}
	// TWO, and the number is the shape rather than an accident. The handler can
	// fail to obtain the list of event names in two ways: the platform call
	// raised, or it answered something the names cannot be looked up in. Both are
	// the same thing to the caller and both carry the same prefix, which is why
	// one remedy serves them; a count that drifted off two means a site was added
	// or lost without anyone deciding which of the two it is.
	if eventFilter != 2 {
		t.Errorf("the module has %d event filter refusals at status %d, want exactly two: the "+
			"platform call raising, and it answering a shape the names cannot be looked up in",
			eventFilter, eventLogRefusalStatus)
	}
	t.Logf("drove %d literal refusals from ЖурналРегистрацииPOST; %d rights, %d user filter, "+
		"%d event filter, at status %d",
		len(refusals), rights, filter, eventFilter, eventLogRefusalStatus)
}

// TestEventLogRefusalPinCanFail is the positive control for the pin above.
//
// Every check in it is of the form «the module says X, so the renderer must
// answer Y». Such a test passes when the predicates match nothing and no branch
// is entered, so the predicates are exercised here against inputs where they
// MUST report a difference.
func TestEventLogRefusalPinCanFail(t *testing.T) {
	// The predicates must not be true of each other's text, otherwise the switch
	// above lands two refusals in the same branch and cannot tell them apart at
	// all.
	if isEventLogRightsRefusal(eventLogUserFilterRefusalPrefix) {
		t.Error("isEventLogRightsRefusal accepts the user filter diagnostic, so those two 403s " +
			"are still indistinguishable")
	}
	if isEventLogUserFilterRefusal(eventLogRightsRefusalPrefix) {
		t.Error("isEventLogUserFilterRefusal accepts the rights diagnostic, so those two 403s " +
			"are still indistinguishable")
	}
	// The third is checked against both of the others, in both directions. The
	// user filter and the event filter diagnostics differ in their FIRST WORD
	// only, so a predicate that matched on «filter cannot be resolved» would
	// accept both and land them in one branch.
	for _, c := range []struct {
		name string
		got  bool
	}{
		{"isEventLogEventFilterRefusal accepts the rights diagnostic",
			isEventLogEventFilterRefusal(eventLogRightsRefusalPrefix)},
		{"isEventLogEventFilterRefusal accepts the user filter diagnostic",
			isEventLogEventFilterRefusal(eventLogUserFilterRefusalPrefix)},
		{"isEventLogRightsRefusal accepts the event filter diagnostic",
			isEventLogRightsRefusal(eventLogEventFilterRefusalPrefix)},
		{"isEventLogUserFilterRefusal accepts the event filter diagnostic",
			isEventLogUserFilterRefusal(eventLogEventFilterRefusalPrefix)},
	} {
		if c.got {
			t.Errorf("%s, so the three 403s on this endpoint are not told apart", c.name)
		}
	}
	// None may be true of everything.
	for _, notARefusal := range []string{"", "   ", "что угодно", "limit must be a number"} {
		if isEventLogRightsRefusal(notARefusal) {
			t.Errorf("isEventLogRightsRefusal(%q) = true, so the classifier accepts anything",
				notARefusal)
		}
		if isEventLogUserFilterRefusal(notARefusal) {
			t.Errorf("isEventLogUserFilterRefusal(%q) = true, so the classifier accepts anything",
				notARefusal)
		}
		if isEventLogEventFilterRefusal(notARefusal) {
			t.Errorf("isEventLogEventFilterRefusal(%q) = true, so the classifier accepts anything",
				notARefusal)
		}
	}
	// And the renderer must key on the STATUS as well as on the text: the same
	// diagnostic at another status is not this refusal.
	other := renderFailure(headingEventLog, &onec.StatusError{
		StatusCode: 500, BodyKind: onec.BodyKindExtension, Detail: eventLogRightsRefusalPrefix,
	})
	if strings.Contains(other, "Это отказ по правам учётной записи, а не по отбору") {
		t.Errorf("the rights remedy is given for status 500, so moving the refusal off %d in the "+
			"module would not be noticed:\n%s", eventLogRefusalStatus, other)
	}
	// The regexp the walk rests on must be able to read a status other than the
	// one the module currently ships, or a status change would look like a
	// missing site rather than like a moved one.
	m := refusalSiteRE.FindStringSubmatch(`Возврат ОтветОшибка(500, "что угодно");`)
	if m == nil || m[1] != "500" || m[2] != "что угодно" {
		t.Errorf("the refusal extractor cannot read a 500 site: %v", m)
	}
}
