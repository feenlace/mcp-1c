package main

import (
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
)

// instrPlainRoot writes a dump root that is one by KIND-DIRECTORY COUNT and carries
// no manifest of its own. That shape matters twice: it is what dumproot.go counts as
// a root without a manifest, and manifestAbsent is the only verdict the layout
// detection descends from, so it is also the only root shape that can hold a
// recognised extension one level in. A root written with a Configuration.xml instead
// answers manifestNotExtension, the descent never runs, and the tree reaches none of
// the branches under test.
func instrPlainRoot(t *testing.T, parent, root string) {
	t.Helper()
	nestedIdentityWrite(t, parent,
		root+"/Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl", "// "+root+" объект\n")
	nestedIdentityWrite(t, parent,
		root+"/CommonModules/Общий/Ext/Module.bsl", "// "+root+" общий\n")
}

// instrExtension writes a recognised extension dump at rel, with one module so the
// index has something to keep or lose.
func instrExtension(t *testing.T, parent, rel, name string) {
	t.Helper()
	nestedIdentityWrite(t, parent, rel+"/Configuration.xml", nestedIdentityExtManifest(name))
	nestedIdentityWrite(t, parent, rel+"/CommonModules/Доп/Ext/Module.bsl", "// "+name+"\n")
}

// instrKeys builds an index over dir and returns its module keys, sorted, with the
// collapse report the same load published.
func instrKeys(t *testing.T, dir string) ([]string, dump.CollapsedKeyState) {
	t.Helper()
	idx, err := dump.NewIndex(dir, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex(%s): %v", dir, err)
	}
	t.Cleanup(func() { idx.Close() })
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("BuildError(%s): %v", dir, err)
	}
	names := append([]string(nil), idx.ModuleNames()...)
	sort.Strings(names)
	return names, idx.CollapsedKeys()
}

type instrCase struct {
	name  string
	build func(t *testing.T, parent string)

	// The measured premise. Without it a green can come from a tree that reaches
	// none of the branches under test.
	roots   []string
	exts    int
	outside int

	// The sentences, asserted in BOTH directions on every fixture.
	wantPathIDs bool // «относительные пути файлов»
	wantRemedy  bool // «Укажите в --dump»
	wantSafe    bool // «не потеряется»
	// The unrecognised sentence has two spellings, one per arity, and they are
	// asserted apart: a single field would let a multi-root tree satisfy it with
	// the single-root wording and vice versa.
	wantOneUnrecognised bool // «Он не опознан как выгрузка расширения»
	wantAllUnrecognised bool // «Ни один из них не опознан как выгрузка расширения»

	// Check the actual count and IDs when the message describes qualification.
	wantModules int
	// wantModuleKey is a key the index must publish for the same tree, read off
	// the same build as the collapse report above.
	wantModuleKey string

	// repoint is the nested root the instruction would send the operator to. The
	// test indexes it and compares key multisets: repointKeeps is what the
	// instruction costs, measured rather than asserted.
	repoint      string
	repointKeeps bool
}

func instrCases() []instrCase {
	const ext = "Доработка"
	return []instrCase{{
		// The path recommendation would discard the extension in the wrapper.
		name: "две простые выгрузки и расширение в стороне",
		build: func(t *testing.T, parent string) {
			instrPlainRoot(t, parent, "A")
			instrPlainRoot(t, parent, "B")
			instrExtension(t, parent, "Обёртка/"+ext, ext)
		},
		roots: []string{"A", "B"}, exts: 1, outside: 1,
		wantPathIDs: true, wantRemedy: false, wantSafe: false,
		wantOneUnrecognised: false, wantAllUnrecognised: true,
		wantModules: 5,
		repoint:     "A", repointKeeps: false,
	}, {
		// The control for it, and it MOVES: the only difference from the case above
		// is a directory that has nothing to do with either root.
		name: "тот же путь без расширения в стороне",
		build: func(t *testing.T, parent string) {
			instrPlainRoot(t, parent, "A")
			instrPlainRoot(t, parent, "B")
		},
		roots: []string{"A", "B"}, exts: 0, outside: 0,
		wantPathIDs: true, wantRemedy: true, wantSafe: false,
		wantOneUnrecognised: false, wantAllUnrecognised: true,
		wantModules: 4,
	}, {
		// A-6. Nothing is lost, so nothing is diagnosed, and the remedy is free:
		// indexing the root directly gives the same keys.
		name: "один корень и расширение внутри него",
		build: func(t *testing.T, parent string) {
			instrPlainRoot(t, parent, "R")
			instrExtension(t, parent, "R/"+ext, ext)
		},
		roots: []string{"R"}, exts: 1, outside: 0,
		wantPathIDs: false, wantRemedy: true, wantSafe: false,
		wantOneUnrecognised: true, wantAllUnrecognised: false,
		repoint: "R", repointKeeps: true,
	}, {
		// The same extension one directory over. The root is still unrecognised and
		// the operator is still told so; only the remedy is withheld, and it is
		// withheld BY DESIGN rather than by a defect.
		name: "один корень и расширение рядом с ним",
		build: func(t *testing.T, parent string) {
			instrPlainRoot(t, parent, "R")
			instrExtension(t, parent, "Обёртка/"+ext, ext)
		},
		roots: []string{"R"}, exts: 1, outside: 1,
		wantPathIDs: false, wantRemedy: false, wantSafe: false,
		wantOneUnrecognised: true, wantAllUnrecognised: false,
		repoint: "R", repointKeeps: false,
	}, {
		// No extension anywhere, which is the shape both the tip and v1.18.0 already
		// answered. It must keep answering the same way.
		name: "один простой корень и ни одного расширения",
		build: func(t *testing.T, parent string) {
			instrPlainRoot(t, parent, "R")
		},
		roots: []string{"R"}, exts: 0, outside: 0,
		wantPathIDs: false, wantRemedy: true, wantSafe: false,
		wantOneUnrecognised: true, wantAllUnrecognised: false,
	}, {
		// Two plain roots need qualified IDs. The extension inside one of
		// them keeps its own namespace.
		name: "две простые выгрузки и расширение внутри одной из них",
		build: func(t *testing.T, parent string) {
			instrPlainRoot(t, parent, "A")
			instrPlainRoot(t, parent, "B")
			instrExtension(t, parent, "A/"+ext, ext)
		},
		roots: []string{"A", "B"}, exts: 1, outside: 0,
		wantPathIDs: true, wantRemedy: true, wantSafe: false,
		wantOneUnrecognised: false, wantAllUnrecognised: true,
		wantModules:   5,
		wantModuleKey: "ext." + ext + ".ОбщийМодуль.Доп.Модуль",
	}}
}

// TestTheDiagnosisAndTheRemedyAreDecidedSeparately.
func TestTheDiagnosisAndTheRemedyAreDecidedSeparately(t *testing.T) {
	for _, c := range instrCases() {
		t.Run(c.name, func(t *testing.T) {
			parent := t.TempDir()
			c.build(t, parent)

			insp := dump.InspectDumpRoot(parent)
			layout := dump.InspectExtensionLayout(parent)

			// PREMISE.
			if insp.IsRoot {
				t.Fatalf("the path inspected AS a root, so the nested-root sentence is unreachable")
			}
			if !slices.Equal(insp.NestedRoots, c.roots) {
				t.Fatalf("NestedRoots = %q, want %q", insp.NestedRoots, c.roots)
			}
			if layout.Extensions != c.exts {
				t.Fatalf("layout recognised %d extensions, want %d. Dirs: %q",
					layout.Extensions, c.exts, layout.Dirs)
			}
			if len(layout.Dirs) != layout.Extensions {
				t.Fatalf("layout named %d directories while counting %d extensions: %+v",
					len(layout.Dirs), layout.Extensions, layout)
			}
			if layout.Undecided() != 0 || layout.ScanTruncated {
				t.Fatalf("the layout carries doubts (%d undecided, truncated=%v), so a second "+
					"record is published and the assertions below read the wrong one",
					layout.Undecided(), layout.ScanTruncated)
			}
			if got := extensionsOutsideRoots(insp.NestedRoots, layout); got != c.outside {
				t.Fatalf("extensionsOutsideRoots = %d, want %d. Roots %q, extension dirs %q",
					got, c.outside, insp.NestedRoots, layout.Dirs)
			}

			// THE SENTENCES, through the real reporting path.
			msg := nestedIdentityReport(t, parent)
			for _, probe := range []struct {
				sub  string
				want bool
			}{
				{"затира", false},
				{"относительные пути файлов", c.wantPathIDs},
				{"Укажите в --dump", c.wantRemedy},
				{"не потеряется", c.wantSafe},
				{"Он не опознан как выгрузка расширения", c.wantOneUnrecognised},
				{"Ни один из них не опознан как выгрузка расширения", c.wantAllUnrecognised},
			} {
				if got := strings.Contains(msg, probe.sub); got != probe.want {
					t.Errorf("«%s» present=%v, want %v.\nMessage: %s", probe.sub, got, probe.want, msg)
				}
			}

			if c.wantModules != 0 {
				names, st := instrKeys(t, parent)
				if len(names) != c.wantModules || len(slices.Compact(slices.Clone(names))) != c.wantModules ||
					st.Files != 0 || st.Keys != 0 {
					t.Errorf("want %d distinct modules and no lost files, got names=%q collapse=%+v",
						c.wantModules, names, st)
				}
				if c.wantModuleKey != "" && !slices.Contains(names, c.wantModuleKey) {
					t.Errorf("the index does not publish %q, so the recognised extension "+
						"inside a root did not keep its own namespace. Keys: %q",
						c.wantModuleKey, names)
				}
			}

			// AND WHAT THE REMEDY WOULD COST, ALSO ON THE SAME TREE.
			if c.repoint != "" {
				at, _ := instrKeys(t, parent)
				moved, movedSt := instrKeys(t, filepath.Join(parent, c.repoint))
				kept := slices.Equal(at, moved)
				if kept != c.repointKeeps {
					t.Errorf("re-pointing --dump at %q keeps the key multiset: %v, want %v.\n"+
						"at the path: %v\nat the root: %v", c.repoint, kept, c.repointKeeps, at, moved)
				}
				if c.repointKeeps && movedSt.Files != 0 {
					t.Errorf("the remedy is offered as free and the root it names loses %d files",
						movedSt.Files)
				}
				if kept != c.wantRemedy {
					t.Errorf("the remedy is offered (%v) on a tree where it keeps everything (%v), "+
						"so the sentence and the measurement disagree.\nMessage: %s",
						c.wantRemedy, kept, msg)
				}
			}
		})
	}
}

// instrDigits is the one number any startup sentence can carry: a count of roots or
// of symlinked children.
var instrDigits = regexp.MustCompile(`[0-9]+`)

// instrSentences splits a startup message into sentences. No sentence this function
// emits carries an inner ". ", and the counts are normalised so a census compares
// wording rather than arithmetic.
func instrSentences(msg string) []string {
	var out []string
	for _, s := range strings.SplitAfter(msg, ". ") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, instrDigits.ReplaceAllString(s, "N"))
		}
	}
	return out
}

// instrExpectedSentences pins every sentence reachable from the startup message.
func instrExpectedSentences() []string {
	return []string{
		"путь в --dump это символьная ссылка.",
		"Индексатор внутрь ссылки не заходит, поэтому в индекс не попадёт ни один модуль, даже если по ссылке лежит правильная выгрузка.",
		"Укажите настоящий путь к каталогу выгрузки.",
		"путь в --dump указывает на корень выгрузки, но среди его подкаталогов есть символьные ссылки, штук: N.",
		"Индексатор внутрь ссылок не заходит, поэтому их содержимое в индекс не попадёт.",
		"Если модули лежат по ссылке, укажите настоящий путь.",
		"путь в --dump указывает не на корень выгрузки, а на каталог выше него.",
		"Внутри лежит готовый корень выгрузки (имя в поле roots).",
		"Внутри лежат готовые корни выгрузки, N штуки, их имена в поле roots.",
		"Он опознан как выгрузка расширения, и сервер проиндексирует его под собственным именем, так что содержимое не потеряется.",
		"Указывать его в --dump нужно только если вам нужен именно он.",
		"Он не опознан как выгрузка расширения.",
		"Укажите в --dump сам этот корень.",
		"Все они опознаны как выгрузки расширений, и сервер проиндексирует каждую под её собственным именем, так что содержимое не потеряется.",
		"Указывать один из них в --dump нужно только если вам нужен именно он.",
		"Часть из них опознана как выгрузки расширений и будет проиндексирована под их именами.",
		"Укажите в --dump тот корень, который вам нужен.",
		"Ни один из них не опознан как выгрузка расширения.",
		"При совпадении идентификаторов модулей сервер добавляет к ним относительные пути файлов.",
		"Сервер не переходит внутрь сам, потому что выбрать за вас не может, а молчаливый переход скрыл бы ошибку пути ровно так же, как она скрывалась до сих пор.",
		"Просмотрены не все подкаталоги, поэтому список может быть неполным.",
	}
}

// instrEveryMessage sweeps the two structs the function reads and collects every
// message it produces.
func instrEveryMessage() []string {
	var msgs []string
	add := func(insp dump.DumpRootInspection, layout dump.ExtensionLayoutSummary) {
		if m := nestedDumpRootMessage(insp, layout); m != "" {
			msgs = append(msgs, m)
		}
	}
	add(dump.DumpRootInspection{RootIsSymlink: true}, dump.ExtensionLayoutSummary{})
	add(dump.DumpRootInspection{IsRoot: true}, dump.ExtensionLayoutSummary{})
	add(dump.DumpRootInspection{IsRoot: true, SymlinkedChildren: 3}, dump.ExtensionLayoutSummary{})
	add(dump.DumpRootInspection{}, dump.ExtensionLayoutSummary{})

	names := []string{"К1", "К2", "К3"}
	for n := 1; n <= len(names); n++ {
		roots := names[:n]
		for recognised := 0; recognised <= n; recognised++ {
			for inside := 0; inside <= 2; inside++ {
				for outside := 0; outside <= 2; outside++ {
					var dirs []string
					dirs = append(dirs, roots[:recognised]...)
					for i := range inside {
						dirs = append(dirs, roots[0]+"/вложение"+strconv.Itoa(i))
					}
					for i := range outside {
						dirs = append(dirs, "иное"+strconv.Itoa(i)+"/расширение")
					}
					for _, truncated := range []bool{false, true} {
						add(
							dump.DumpRootInspection{
								NestedRoots: append([]string(nil), roots...),
								Truncated:   truncated,
							},
							dump.ExtensionLayoutSummary{Extensions: len(dirs), Dirs: dirs},
						)
					}
				}
			}
		}
	}
	return msgs
}

func TestStartupMessageSentenceCensus(t *testing.T) {
	expected := map[string]bool{}
	for _, s := range instrExpectedSentences() {
		if _, duplicate := expected[s]; duplicate {
			t.Fatalf("the expected census lists a sentence twice, so one of the two "+
				"directions below is vacuous: %q", s)
		}
		expected[s] = false
	}

	msgs := instrEveryMessage()
	if len(msgs) < len(expected) {
		t.Fatalf("the sweep produced %d messages for %d sentences, which cannot cover them",
			len(msgs), len(expected))
	}

	seen := map[string]bool{}
	for _, msg := range msgs {
		sentences := instrSentences(msg)
		if len(sentences) == 0 {
			t.Fatalf("a non-empty message split into no sentences at all: %q", msg)
		}
		for _, s := range sentences {
			if _, ok := expected[s]; !ok {
				t.Errorf("unexpected startup sentence:\n%q\nin: %s", s, msg)
			}
			seen[s] = true
		}
	}
	for _, s := range instrExpectedSentences() {
		if !seen[s] {
			t.Errorf("expected startup sentence is unreachable: %q", s)
		}
	}

	// PREMISE for the split: a sentence that never got split would make the census
	// above compare whole arms and pass while the halves went missing.
	if got := instrSentences("Раз. Два. Три."); len(got) != 3 {
		t.Fatalf("the splitter returned %d sentences for a three-sentence string: %q", len(got), got)
	}

	// AND NO ТИРЕ ANYWHERE IN THE CENSUS, over the set this package already keeps.
	if len(dashRunes) < 5 {
		t.Fatalf("dashRunes holds %d runes; the scan is only as wide as this set", len(dashRunes))
	}
	if !strings.ContainsAny("Доработки — копия", string(dashRunes)) {
		t.Fatal("control failed: the sample carries none of the dash characters, so a clean " +
			"scan below would be the scan being blind")
	}
	scanned := 0
	for s := range seen {
		for _, got := range s {
			scanned++
			for _, bad := range dashRunes {
				if got == bad {
					t.Errorf("customer-facing RU carries U+%04X: %s", bad, s)
				}
			}
		}
	}
	if scanned == 0 {
		t.Fatal("the dash scan read no codepoints at all")
	}
}
