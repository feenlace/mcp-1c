package tools

import (
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
)

// The wrap notice on the tree of issue 46, measured where the operator reads it.
//
// WHAT THE FIX LEFT BEHIND. The descent in dump/extlayout.go now gives an extension
// two levels below the dump root its own namespace, keyed by a two-segment prefix.
// dump/wrapped_paths.go:wrapDepth was not taught about that map: it asks byDir
// whether the FIRST segment is a recognised extension directory and asks nothing
// else, so for a path the namespace fully accounts for it still handed the whole
// path to anchorIndex, which moved, and the file counted as wrapped. The keys were
// right and every search_code answer carried a notice telling the operator their
// --dump is pointed above the dump root and to restart against the root, which is
// the one thing that would take the namespace away again.
//
// WHY THE ASSERTION IS ON THE RENDERED ANSWER AND NOT ON THE COUNTER ALONE. The
// counter is one input of indexNotices; the sentence is what ships. These tests go
// through NewSearchCodeHandler on an index opened the way a serving process opens
// one, so the warm read-only path is exercised as well: the layout there is
// resolved by dump.Index.layout and not by key derivation, and a wrap report taken
// against an empty layout is the defect that shipped once already.
//
// AND A ZERO IS NOT ENOUGH ON ITS OWN. A counter that has been silenced and one
// that is correct look identical from a single zero, so the pair below is one
// commit: the tree the fix repairs must go quiet, and a tree that really is
// wrapped must still be counted and still be reported.

const (
	// The base configuration's own document module, and the same logical module
	// inside the extension. The namespace comes from <Name> in the manifest.
	issue46NoticeBaseKey = "Документ.ПеремещениеЗапасов.МодульОбъекта"
	issue46NoticeExtKey  = "ext.ИнтеграцияMach3.Документ.ПеремещениеЗапасов.МодульОбъекта"

	// The extension's declared name, which is neither of its directory names.
	issue46NoticeExtName = "ИнтеграцияMach3"
	// The two-segment prefix, relative to the dump root, at which it is rooted.
	issue46NoticeExtPrefix = "Mach3/extension"
)

// issue46NoticeManifest is the byte shape 1C writes for an extension's
// Configuration.xml: UTF-8 WITH a BOM, CRLF, the marker inside <Properties> and
// <Name> right after it. Written out here rather than shared with the dump
// package's fixture builder, which is an unexported test helper of that package.
func issue46NoticeManifest(name string) string {
	return "\ufeff<?xml version=\"1.0\" encoding=\"UTF-8\"?>\r\n" +
		"<MetaDataObject xmlns=\"http://v8.1c.ru/8.3/MDClasses\" version=\"2.20\">\r\n" +
		"\t<Configuration uuid=\"4903dffe-5f70-488e-882c-436c910a1d05\">\r\n" +
		"\t\t<Properties>\r\n" +
		"\t\t\t<ObjectBelonging>Adopted</ObjectBelonging>\r\n" +
		"\t\t\t<Name>" + name + "</Name>\r\n" +
		"\t\t\t<ConfigurationExtensionPurpose>Customization</ConfigurationExtensionPurpose>\r\n" +
		"\t\t</Properties>\r\n" +
		"\t</Configuration>\r\n</MetaDataObject>\r\n"
}

// issue46NoticeBaseManifest is a CONFIGURATION's own manifest: it declares neither
// ObjectBelonging nor a purpose, and that absence is the discriminator.
func issue46NoticeBaseManifest() string {
	return "\ufeff<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
		"<MetaDataObject xmlns=\"http://v8.1c.ru/8.3/MDClasses\" version=\"2.20\">\n" +
		"\t<Configuration uuid=\"aaaa\">\n\t\t<Properties>\n" +
		"\t\t\t<Name>УправлениеТорговлей</Name>\n" +
		"\t\t</Properties>\n\t</Configuration>\n</MetaDataObject>\n"
}

// issue46NoticeBody carries collapseTerm, so callSearchCollapse finds it and the
// silence asserted below is the silence of a real answer rather than of «nothing
// found».
func issue46NoticeBody(n string) string {
	return "Процедура " + n + "()\n    Сообщить(\"" + collapseTerm + "\");\nКонецПроцедуры\n"
}

// TestIssue46_WrappedNoticeIsSilentOnceTheExtensionIsNamed is the regression: the
// tree of issue 46, whose extension now HAS a namespace, must stop being reported
// as a dump pointed above its root.
func TestIssue46_WrappedNoticeIsSilentOnceTheExtensionIsNamed(t *testing.T) {
	// Every key this package's index emits has been through NFC, so a decomposed
	// literal here could never match one and the test would fail for a reason with
	// nothing to do with the defect. Checked rather than assumed.
	for _, k := range []string{issue46NoticeBaseKey, issue46NoticeExtKey} {
		if dump.NFC(k) != k {
			t.Fatalf("test literal %q is not NFC, so it can never match an index key", k)
		}
	}

	root := t.TempDir()
	// The base configuration: its own manifest and one document module.
	mkBSL(t, root, "Configuration.xml", issue46NoticeBaseManifest())
	mkBSL(t, root, "Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl",
		issue46NoticeBody("Базовый"))
	// The extension, TWO levels down: a container that is not itself an extension,
	// and the extension root inside it, carrying a module that collides with the
	// base configuration's own key unless it is given a namespace.
	mkBSL(t, root, issue46NoticeExtPrefix+"/Configuration.xml",
		issue46NoticeManifest(issue46NoticeExtName))
	mkBSL(t, root, issue46NoticeExtPrefix+"/Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl",
		issue46NoticeBody("Расширенный"))

	idx := collapseIndex(t, root, false)

	// PREMISE, AND IT IS NOT A FORMALITY. «No warning» is satisfied by an index that
	// found nothing at all, and by one that found both files and merged them onto
	// one key. So the namespace is asserted first, by the key AND by the bytes that
	// key serves: two keys resolving to one file would satisfy a key-set check and
	// still have lost a module.
	names := idx.ModuleNames()
	seen := make(map[string]int, len(names))
	for _, n := range names {
		seen[n]++
	}
	if len(names) != 2 {
		t.Fatalf("the index holds %d module entries, want 2: the walk must have reached "+
			"both .bsl files before anything below can mean anything. Names: %v",
			len(names), names)
	}
	if seen[issue46NoticeExtKey] == 0 {
		t.Fatalf("the extension's key %q is absent: the module under %q was given no "+
			"namespace, so the notice below would be right to fire. Names: %v",
			issue46NoticeExtKey, issue46NoticeExtPrefix, names)
	}
	baseGot, baseOK := idx.GetContent(issue46NoticeBaseKey)
	extGot, extOK := idx.GetContent(issue46NoticeExtKey)
	if !baseOK || !extOK {
		t.Fatalf("GetContent could not serve both keys: base ok=%v, ext ok=%v", baseOK, extOK)
	}
	if extGot != issue46NoticeBody("Расширенный") {
		t.Fatalf("%q serves %q, want the extension's own bytes: the key exists but it is "+
			"the wrong file behind it", issue46NoticeExtKey, extGot)
	}
	if baseGot == extGot {
		t.Fatalf("both keys served the same bytes %q: one file behind two keys is the "+
			"collapse the namespace exists to prevent", baseGot)
	}

	// THE COUNTER. Both segments of the extension's prefix are accounted for by the
	// namespace, so nothing here was keyed from above a dump root.
	if wp := idx.WrappedPaths(); wp.Files != 0 {
		t.Errorf("WrappedPaths = %+v, want no wrapped file: the two segments of %q are "+
			"consumed by the namespace the fix gives that extension, exactly as an "+
			"-AllExtensions container's one segment is", wp, issue46NoticeExtPrefix)
	}

	// THE SENTENCE, which is what an operator actually reads.
	text := callSearchCollapse(t, idx)
	for _, marker := range []string{wrappedMarker, collapseMarker, doubtMarker} {
		if strings.Contains(text, marker) {
			t.Errorf("the answer for a correctly namespaced nested extension carries %q:\n%s",
				marker, text)
		}
	}
	// NOT MERELY «no marker»: nothing was prepended at all, so the answer opens on
	// its own header.
	if !strings.HasPrefix(text, "## Результаты поиска") {
		t.Errorf("the answer does not open on the result header, so something was "+
			"prepended to it; it starts with %q", strings.SplitN(text, "\n", 2)[0])
	}
	// AND IT IS STILL AN ANSWER. Without this the silences above are the silence of
	// an empty result.
	if searchRenderedMatches(text) == 0 {
		t.Fatalf("the answer carries no matches at all, so the silence above is about an "+
			"empty answer rather than about a healthy index:\n%s", text)
	}
}

// TestIssue46_TheWrapNoticeStillFiresWhereNoExtensionIsDeclared is the control the
// zero above cannot be on its own.
//
// A silenced counter and a correct one are the same zero. This tree is a dump root
// with one directory above it and NO manifest anywhere, so no namespace accounts
// for that segment, every key is derived from a path the anchor scan had to move,
// and both the counter and the sentence must still say so.
func TestIssue46_TheWrapNoticeStillFiresWhereNoExtensionIsDeclared(t *testing.T) {
	parent := t.TempDir()
	mkBSL(t, parent, "выгрузка/Catalogs/Ном/Ext/ObjectModule.bsl", issue46NoticeBody("Первый"))
	mkBSL(t, parent, "выгрузка/CommonModules/Общий/Ext/Module.bsl", issue46NoticeBody("Второй"))

	idx := collapseIndex(t, parent, false)

	// PREMISE: no extension was recognised, which is what makes every segment above
	// the kind directory unaccounted for.
	if l := idx.ExtensionLayout(); l.Extensions != 0 || l.SelfNamed {
		t.Fatalf("ExtensionLayout = %+v, want nothing recognised: this tree declares no "+
			"manifest anywhere", l)
	}
	for _, n := range idx.ModuleNames() {
		if strings.HasPrefix(n, "ext.") {
			t.Fatalf("premise broken: %q carries a namespace, so there is nothing to "+
				"report about this tree", n)
		}
	}
	wp := idx.WrappedPaths()
	if wp.Total == 0 || wp.Files != wp.Total {
		t.Fatalf("WrappedPaths = %+v, want every file counted as wrapped: the counter has "+
			"been silenced rather than taught", wp)
	}

	text := callSearchCollapse(t, idx)
	if !strings.Contains(text, wrappedMarker) {
		t.Fatalf("a dump pointed above its root produced no notice at all:\n%s", text)
	}
	if searchRenderedMatches(text) == 0 {
		t.Errorf("the answer carries no matches at all:\n%s", text)
	}
}
