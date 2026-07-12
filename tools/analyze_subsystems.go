package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// AnalyzeSubsystemsTool returns the MCP tool definition for analyze_subsystems.
//
// The action parameter is intentionally free-text (no JSON enum): it is
// validated in the handler so an unknown value yields a clear, actionable
// error instead of a schema rejection.
func AnalyzeSubsystemsTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "analyze_subsystems",
		Title:       "Анализ топологии подсистем",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Анализ распределения объектов конфигурации 1С по подсистемам. " +
			"action=orphans: применимые объекты (справочники, документы, регистры, отчёты, обработки, планы, бизнес-процессы, задачи, перечисления), не входящие ни в одну подсистему. " +
			"action=containing: список подсистем, содержащих указанный объект (параметр object, полное имя вида Документ.РеализацияТоваровУслуг или короткое РеализацияТоваровУслуг). " +
			"action=intersections: объекты, входящие сразу в несколько подсистем (при cross_branch_only=true остаются только пересечения между разными корневыми подсистемами). " +
			"Используй для аудита архитектуры: найти неучтённые объекты, понять к каким подсистемам относится объект, выявить дублирование между ветвями.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"description": "Вид анализа: orphans (объекты вне подсистем), containing (подсистемы, содержащие объект), intersections (объекты в нескольких подсистемах)."
				},
				"object": {
					"type": "string",
					"description": "Для action=containing: имя объекта. Полное (Документ.РеализацияТоваровУслуг) или короткое (РеализацияТоваровУслуг). При коротком неоднозначном имени возвращаются все совпадения."
				},
				"object_type": {
					"type": "string",
					"description": "Необязательное уточнение для action=containing при неоднозначном коротком имени: префикс вида метаданных полного имени (Справочник, Документ, РегистрСведений и т.п.) или его английский эквивалент (Catalog, Document, InformationRegister)."
				},
				"cross_branch_only": {
					"type": "boolean",
					"description": "Для action=intersections: если true, оставить только объекты, входящие в подсистемы из разных корневых веток дерева."
				}
			},
			"required": ["action"]
		}`),
	}
}

// analyzeSubsystemsInput is the decoded argument set for analyze_subsystems.
type analyzeSubsystemsInput struct {
	Action          string `json:"action"`
	Object          string `json:"object"`
	ObjectType      string `json:"object_type"`
	CrossBranchOnly bool   `json:"cross_branch_only"`
}

// NewAnalyzeSubsystemsHandler returns a ToolHandler that fetches the subsystem
// forest from 1C once and runs the requested topology analysis on it.
func NewAnalyzeSubsystemsHandler(client *onec.Client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input analyzeSubsystemsInput
		if err := json.Unmarshal(req.Params.Arguments, &input); err != nil {
			return nil, fmt.Errorf("parsing input: %w", err)
		}

		action := strings.TrimSpace(input.Action)
		switch action {
		case "orphans", "containing", "intersections":
			// valid
		case "":
			return nil, fmt.Errorf("action is required (allowed: orphans, containing, intersections)")
		default:
			return nil, fmt.Errorf("unknown action: %q (allowed: orphans, containing, intersections)", input.Action)
		}

		object := strings.TrimSpace(input.Object)
		if action == "containing" && object == "" {
			return nil, fmt.Errorf("action=containing requires the object parameter (full or short metadata name)")
		}

		var forest onec.SubsystemForest
		if err := client.Get(ctx, "/subsystems", &forest); err != nil {
			return nil, fmt.Errorf("fetching subsystems from 1C: %w", err)
		}

		switch action {
		case "orphans":
			return textResult(computeOrphans(forest)), nil
		case "containing":
			return textResult(computeContaining(forest, object, input.ObjectType)), nil
		default: // intersections
			return textResult(computeIntersections(forest, input.CrossBranchOnly)), nil
		}
	}
}

// subsystemRef identifies one subsystem that directly contains an object, along
// with the name of its top-level (root) ancestor in the tree.
type subsystemRef struct {
	name     string // subsystem short name
	fullName string // subsystem full metadata name (may be empty)
	root     string // name of the top-level ancestor subsystem
}

// flattenForest walks the whole subsystem tree and returns a membership index:
// object full name -> every subsystem whose direct Состав lists it, each tagged
// with its root ancestor. Membership is by direct composition only (an object in
// a child subsystem's Состав is NOT implicitly a member of the parent).
func flattenForest(forest onec.SubsystemForest) map[string][]subsystemRef {
	membership := make(map[string][]subsystemRef)

	var walk func(nodes []onec.SubsystemNode, root string)
	walk = func(nodes []onec.SubsystemNode, root string) {
		for _, n := range nodes {
			r := root
			if r == "" {
				r = n.Name // this node is itself a root
			}
			for _, obj := range n.Content {
				membership[obj] = append(membership[obj], subsystemRef{
					name:     n.Name,
					fullName: n.FullName,
					root:     r,
				})
			}
			if len(n.Subsystems) > 0 {
				walk(n.Subsystems, r)
			}
		}
	}
	walk(forest.Subsystems, "")
	return membership
}

// shortName returns the segment after the last dot of a full metadata name,
// e.g. "Документ.РеализацияТоваровУслуг" -> "РеализацияТоваровУслуг".
func shortName(full string) string {
	if i := strings.LastIndex(full, "."); i >= 0 {
		return full[i+1:]
	}
	return full
}

// kindPrefix returns the segment before the first dot of a full metadata name,
// e.g. "Документ.РеализацияТоваровУслуг" -> "Документ".
func kindPrefix(full string) string {
	if i := strings.Index(full, "."); i >= 0 {
		return full[:i]
	}
	return ""
}

// enToRuKind maps English metadata-kind names to the Russian singular prefix
// that 1C's ПолноеИмя() emits. Best-effort convenience for object_type: it only
// ever NARROWS an already-matched candidate set, and computeContaining ignores
// the filter when it excludes everything, so an imperfect entry degrades to
// "no disambiguation" rather than a wrong answer. Keys are lowercase so the
// lookup is case-insensitive (catalog / Catalog / CATALOG all resolve).
var enToRuKind = map[string]string{
	"catalog":                    "Справочник",
	"document":                   "Документ",
	"enum":                       "Перечисление",
	"report":                     "Отчет",
	"dataprocessor":              "Обработка",
	"informationregister":        "РегистрСведений",
	"accumulationregister":       "РегистрНакопления",
	"accountingregister":         "РегистрБухгалтерии",
	"calculationregister":        "РегистрРасчета",
	"chartofaccounts":            "ПланСчетов",
	"chartofcharacteristictypes": "ПланВидовХарактеристик",
	"chartofcalculationtypes":    "ПланВидовРасчета",
	"exchangeplan":               "ПланОбмена",
	"businessprocess":            "БизнесПроцесс",
	"task":                       "Задача",
}

// filterByType keeps only full names whose kind prefix matches objectType,
// accepting either the raw Russian prefix or a known English equivalent. The
// match is case-insensitive on both sides.
func filterByType(candidates []string, objectType string) []string {
	want := strings.ToLower(strings.TrimSpace(objectType))
	if want == "" {
		return candidates
	}
	mapped := strings.ToLower(enToRuKind[want])
	var out []string
	for _, full := range candidates {
		p := strings.ToLower(kindPrefix(full))
		if p == want || (mapped != "" && p == mapped) {
			out = append(out, full)
		}
	}
	return out
}

// dedupeRefs removes duplicate subsystem references (an object listed twice in
// the same subsystem), keeping distinct subsystems apart.
func dedupeRefs(refs []subsystemRef) []subsystemRef {
	seen := make(map[string]bool, len(refs))
	out := make([]subsystemRef, 0, len(refs))
	for _, r := range refs {
		key := r.fullName
		if key == "" {
			key = r.name + "\x00" + r.root
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// sortRefs orders subsystem references deterministically by name, then root,
// then full name.
func sortRefs(refs []subsystemRef) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].name != refs[j].name {
			return refs[i].name < refs[j].name
		}
		if refs[i].root != refs[j].root {
			return refs[i].root < refs[j].root
		}
		return refs[i].fullName < refs[j].fullName
	})
}

// distinctRoots counts the distinct root ancestors among a set of references.
func distinctRoots(refs []subsystemRef) int {
	s := make(map[string]bool, len(refs))
	for _, r := range refs {
		s[r.root] = true
	}
	return len(s)
}

// writeForestWarnings emits a short RU diagnostics line when the 1C universe
// builder reported non-fatal warnings (an applied collection threw while being
// enumerated and was skipped), so a degraded / partial universe is visible to
// the caller instead of being silently trusted as complete. Customer-facing RU:
// no em/en dash.
func writeForestWarnings(b *strings.Builder, forest onec.SubsystemForest) {
	if len(forest.Warnings) == 0 {
		return
	}
	fmt.Fprintf(b, "> Диагностика: универсум объектов неполный, пропущено коллекций: %d. Причины: %s\n\n",
		len(forest.Warnings), strings.Join(forest.Warnings, "; "))
}

// computeOrphans lists applied objects that belong to no subsystem. The universe
// is forest.AllObjects (applied kinds only, chosen server-side); noise objects
// (auto-generated attachments) are filtered out via the shared isNoise so they
// are never flagged. Output uses ASCII list markers, sorted, no тире.
func computeOrphans(forest onec.SubsystemForest) string {
	membership := flattenForest(forest)

	seen := make(map[string]bool)
	var orphans []string
	for _, obj := range forest.AllObjects {
		if isNoise(obj) || seen[obj] {
			continue
		}
		if _, contained := membership[obj]; contained {
			continue
		}
		seen[obj] = true
		orphans = append(orphans, obj)
	}
	// Intentional UTF-8 byte-order sort (not linguistic collation): this output is
	// machine-consumed and must be deterministic across runs and platforms. A
	// linguistic Ё/ё-folding order is deliberately NOT applied (and would pull in
	// a golang.org/x/text/collate dependency we do not want here).
	sort.Strings(orphans)

	var b strings.Builder
	fmt.Fprintf(&b, "# Объекты вне подсистем (%d)\n\n", len(orphans))
	writeForestWarnings(&b, forest)
	if len(forest.AllObjects) == 0 {
		// Distinguish an empty or unavailable universe from genuine full coverage:
		// with no applicable objects at all, "everything is distributed" would be a
		// misleading claim.
		b.WriteString("Универсум применимых объектов пуст или недоступен.\n")
		return b.String()
	}
	if len(orphans) == 0 {
		b.WriteString("Все применимые объекты распределены по подсистемам.\n")
		return b.String()
	}
	for _, o := range orphans {
		fmt.Fprintf(&b, "- %s\n", o)
	}
	return b.String()
}

// computeContaining lists the subsystems that directly contain the given object.
// The object may be a full name (exact match) or a short name (last segment); an
// ambiguous short name matches every object sharing that segment and all are
// listed. object_type optionally disambiguates an ambiguous short name.
func computeContaining(forest onec.SubsystemForest, object, objectType string) string {
	membership := flattenForest(forest)

	// Case-insensitive matching (bug #4): fold case on both the query and the
	// stored names, but keep the original-case full names in the output. The same
	// strings.ToLower normalization filterByType uses is applied here.
	q := strings.ToLower(strings.TrimSpace(object))

	var matched []string
	for full := range membership {
		if strings.ToLower(full) == q {
			matched = append(matched, full) // exact full-name hit (case-folded)
		}
	}
	if len(matched) == 0 {
		for full := range membership {
			if strings.ToLower(shortName(full)) == q {
				matched = append(matched, full)
			}
		}
		if objectType != "" && len(matched) > 1 {
			if f := filterByType(matched, objectType); len(f) > 0 {
				matched = f
			}
		}
	}
	sort.Strings(matched)

	total := 0
	for _, full := range matched {
		total += len(dedupeRefs(membership[full]))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Подсистемы, содержащие %s (%d)\n\n", object, total)
	writeForestWarnings(&b, forest)
	if len(matched) == 0 {
		b.WriteString("Объект не найден ни в одной подсистеме.\n")
		return b.String()
	}
	for _, full := range matched {
		fmt.Fprintf(&b, "## %s\n", full)
		refs := dedupeRefs(membership[full])
		sortRefs(refs)
		for _, r := range refs {
			fmt.Fprintf(&b, "- %s (корень: %s)\n", r.name, r.root)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// computeIntersections lists objects that belong to two or more subsystems. When
// crossBranchOnly is set, only objects whose subsystems span two or more distinct
// root branches are kept. Each object is shown with its subsystems and roots.
func computeIntersections(forest onec.SubsystemForest, crossBranchOnly bool) string {
	membership := flattenForest(forest)

	type entry struct {
		object string
		refs   []subsystemRef
	}
	var entries []entry
	for obj, refs := range membership {
		if isNoise(obj) {
			continue // parity with computeOrphans: auto-generated noise is never reported
		}
		d := dedupeRefs(refs)
		if len(d) < 2 {
			continue
		}
		if crossBranchOnly && distinctRoots(d) < 2 {
			continue
		}
		entries = append(entries, entry{object: obj, refs: d})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].object < entries[j].object })

	var b strings.Builder
	fmt.Fprintf(&b, "# Объекты в нескольких подсистемах (%d)\n\n", len(entries))
	writeForestWarnings(&b, forest)
	if len(entries) == 0 {
		if crossBranchOnly {
			b.WriteString("Нет объектов, входящих в подсистемы из разных корневых веток.\n")
		} else {
			b.WriteString("Нет объектов, входящих более чем в одну подсистему.\n")
		}
		return b.String()
	}
	for _, e := range entries {
		sortRefs(e.refs)
		fmt.Fprintf(&b, "## %s\n", e.object)
		for _, r := range e.refs {
			fmt.Fprintf(&b, "- %s (корень: %s)\n", r.name, r.root)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
