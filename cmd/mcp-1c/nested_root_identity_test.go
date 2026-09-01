package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
)

// The startup sentence names ONE directory, so it must be decided by evidence
// about THAT directory.
//
// WHAT MOVED UNDER IT. dump.ExtensionLayoutSummary.Extensions used to count the
// inspected path's IMMEDIATE children only, and an immediate child that declares an
// extension carries Configuration.xml, which is the very thing dumproot.go reads as
// «this child is a dump root». So «one root below the path» and «one extension
// below the path» could not name different directories, and the switch in
// nestedDumpRootMessage was entitled to read the count as an identity. The descent
// added for issue 46 also counts GRANDCHILDREN, and a grandchild is never an
// element of DumpRootInspection.NestedRoots, which holds immediate children and
// nothing else. The count therefore stopped answering the question the sentence
// asks, while the sentence went on reading it as if it still did.
//
// WHAT THE OPERATOR IS TOLD WHEN IT IS WRONG: «Он опознан как выгрузка расширения,
// и сервер проиндексирует его под собственным именем», about a directory that will
// be keyed into the base keyspace with no namespace of any kind. The evidence
// behind that sentence belongs to a directory one level lower which the sentence
// never mentions.
//
// THE TREES BELOW ARE UNMODIFIED PLATFORM OUTPUT UNDER ONE PATH. `main` is what
// DumpConfigToFiles writes for a configuration and `ext` is what it writes with
// -AllExtensions, which is a container with one subdirectory per extension and no
// manifest of its own. A container like that is NOT a dump root, so it does not
// appear in NestedRoots at all, and the only root below the path is the
// configuration. Pointing --dump at the directory that holds both is the mistake
// this whole message exists for; dumproot.go opens with a customer who did exactly
// that.
//
// THE CONTROL IS THE SAME TWO NUMBERS. Both trees inspect as one root below the
// path and one extension recognised below the path, so nothing a COUNT can see
// tells them apart. That is the defect stated as a measurement, and it is why the
// repair has to compare directories rather than totals.

// nestedIdentityExtManifest is the byte shape 1C writes for an extension's
// Configuration.xml: UTF-8 WITH a BOM, CRLF, the marker inside <Properties> and
// <Name> right after it. Written out here rather than shared with the dump
// package's fixture builder, which is an unexported test helper of that package.
func nestedIdentityExtManifest(name string) string {
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

// nestedIdentityBaseManifest is a CONFIGURATION's own manifest: it declares neither
// ObjectBelonging nor a purpose, and that absence is the discriminator.
func nestedIdentityBaseManifest() string {
	return "\ufeff<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
		"<MetaDataObject xmlns=\"http://v8.1c.ru/8.3/MDClasses\" version=\"2.20\">\n" +
		"\t<Configuration uuid=\"aaaa\">\n\t\t<Properties>\n" +
		"\t\t\t<Name>УправлениеТорговлей</Name>\n" +
		"\t\t</Properties>\n\t</Configuration>\n</MetaDataObject>\n"
}

// nestedIdentityWrite writes one file, creating the directories above it.
func nestedIdentityWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// nestedIdentityReport drives the REAL reporting path, the one main calls with the
// resolved flag, and returns the sentence the operator would read. Asserting on the
// formatter alone would prove nothing about what is delivered: the caller decides
// which sentence to ask for.
func nestedIdentityReport(t *testing.T, dir string) string {
	t.Helper()
	out := captureAtErrorLevel(t)
	reportDumpRootAndLayout(dir)
	rec := out.String()
	if rec == "" {
		t.Fatalf("%s produced no startup record at all, so there is no sentence to check", dir)
	}
	return recordMessage(t, rec)
}

// nestedIdentityPremise asserts the shape every case below depends on: the path is
// not a root, exactly one root sits below it, and exactly one directory below it
// was recognised as an extension. Without it a green could be reached by a tree
// that reaches none of the branches under test.
func nestedIdentityPremise(t *testing.T, dir string) (dump.DumpRootInspection, dump.ExtensionLayoutSummary) {
	t.Helper()
	insp := dump.InspectDumpRoot(dir)
	layout := dump.InspectExtensionLayout(dir)
	if insp.IsRoot {
		t.Fatalf("%s inspected AS a root, so the nested-root sentence is unreachable", dir)
	}
	if len(insp.NestedRoots) != 1 {
		t.Fatalf("NestedRoots = %q, want exactly one root below the path", insp.NestedRoots)
	}
	if layout.Extensions != 1 {
		t.Fatalf("layout recognised %d extensions, want exactly one. Full summary: %+v",
			layout.Extensions, layout)
	}
	if len(layout.Dirs) != 1 {
		t.Fatalf("layout named %d extension directories while counting %d, so the count and "+
			"the names disagree: %+v", len(layout.Dirs), layout.Extensions, layout)
	}
	// The doubt sentence must stay out of this: a second record in the buffer would
	// make recordMessage read whichever one came first.
	if layout.Undecided() != 0 || layout.ScanTruncated {
		t.Fatalf("the layout carries doubts (%d undecided, truncated=%v), so a second "+
			"record is published and the assertions below read the wrong one",
			layout.Undecided(), layout.ScanTruncated)
	}
	return insp, layout
}

// TestNestedRootIsNotCalledAnExtensionOnEvidenceFromAnotherDirectory.
func TestNestedRootIsNotCalledAnExtensionOnEvidenceFromAnotherDirectory(t *testing.T) {
	const extName = "ИнтеграцияMach3"

	// THE CONTROL FIRST, because every assertion below is a comparison against it.
	// Here the root below the path really is the extension: it carries the manifest
	// itself, so the sentence that names it is true and MUST survive the repair.
	// Without this a fix could simply delete the branch and pass.
	control := t.TempDir()
	nestedIdentityWrite(t, control, "Доработки/Configuration.xml", nestedIdentityExtManifest(extName))
	nestedIdentityWrite(t, control, "Доработки/Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl",
		"// расширение\n")

	ctrlInsp, ctrlLayout := nestedIdentityPremise(t, control)
	if ctrlLayout.Dirs[0] != ctrlInsp.NestedRoots[0] {
		t.Fatalf("the control recognised %q while the root below the path is %q; the control "+
			"is supposed to be the case where they are the SAME directory",
			ctrlLayout.Dirs[0], ctrlInsp.NestedRoots[0])
	}
	ctrlMsg := nestedIdentityReport(t, control)
	if !strings.Contains(ctrlMsg, "Он опознан как выгрузка расширения") {
		t.Fatalf("the root below the path IS the extension and the message does not say so, "+
			"so the case that must keep working is already broken:\n%s", ctrlMsg)
	}

	cases := []struct {
		name  string
		build func(t *testing.T) string
	}{
		{
			// The root's own manifest was READ and answered «not an extension». The
			// message says the opposite while holding that answer.
			name: "the root declared it is not an extension",
			build: func(t *testing.T) string {
				parent := t.TempDir()
				// main: what DumpConfigToFiles writes for a configuration.
				nestedIdentityWrite(t, parent, "main/Configuration.xml", nestedIdentityBaseManifest())
				nestedIdentityWrite(t, parent, "main/ConfigDumpInfo.xml", "<x/>")
				nestedIdentityWrite(t, parent, "main/Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl",
					"// базовый\n")
				nestedIdentityWrite(t, parent, "main/Catalogs/Номенклатура/Ext/ObjectModule.bsl",
					"// базовый\n")
				// ext: what DumpConfigToFiles writes with -AllExtensions. The container
				// carries no manifest and is not a dump root; the extension is inside it.
				nestedIdentityWrite(t, parent, "ext/"+extName+"/Configuration.xml",
					nestedIdentityExtManifest(extName))
				nestedIdentityWrite(t, parent, "ext/"+extName+"/Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl",
					"// расширение\n")
				return parent
			},
		},
		{
			// The root's own manifest was NEVER READ: it is a root because enough of
			// its children are metadata kind directories, which is the branch
			// dumproot.go keeps for a dump with no manifest at any depth. The
			// extension sits one level inside it.
			name: "the root was never asked",
			build: func(t *testing.T) string {
				parent := t.TempDir()
				nestedIdentityWrite(t, parent, "main/Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl",
					"// базовый\n")
				nestedIdentityWrite(t, parent, "main/Catalogs/Номенклатура/Ext/ObjectModule.bsl",
					"// базовый\n")
				nestedIdentityWrite(t, parent, "main/Доработки/Configuration.xml",
					nestedIdentityExtManifest(extName))
				nestedIdentityWrite(t, parent, "main/Доработки/Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl",
					"// расширение\n")
				return parent
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.build(t)
			insp, layout := nestedIdentityPremise(t, dir)

			// THE PREMISE THAT MAKES THIS A DEFECT AND NOT A PREFERENCE: the
			// directory the detection recognised is NOT the root the sentence is
			// about.
			if layout.Dirs[0] == insp.NestedRoots[0] {
				t.Fatalf("the recognised extension %q IS the root below the path, so this "+
					"tree does not reach the case under test", layout.Dirs[0])
			}

			// AND THE COUNTS ARE THE CONTROL'S COUNTS, EXACTLY. A sentence chosen from
			// these two numbers cannot be right in both trees, whatever it says.
			if len(insp.NestedRoots) != len(ctrlInsp.NestedRoots) || layout.Extensions != ctrlLayout.Extensions {
				t.Fatalf("this tree measures (roots=%d, extensions=%d) against the control's "+
					"(roots=%d, extensions=%d); they must be identical for the comparison "+
					"below to mean anything", len(insp.NestedRoots), layout.Extensions,
					len(ctrlInsp.NestedRoots), ctrlLayout.Extensions)
			}
			t.Logf("roots=%q recognised=%q", insp.NestedRoots, layout.Dirs)

			msg := nestedIdentityReport(t, dir)

			if strings.Contains(msg, "Он опознан как выгрузка расширения") {
				t.Errorf("the message calls the root below the path an extension dump. The "+
					"only extension recognised here is %q, which is not that root (%q), and "+
					"nothing read the root's own manifest and got «yes»:\n%s",
					layout.Dirs[0], insp.NestedRoots[0], msg)
			}
			if strings.Contains(msg, "проиндексирует его под собственным именем") {
				t.Errorf("the message promises the root below the path a namespace of its "+
					"own. dump.extensionLayout.moduleKey gives one only to a path under a "+
					"recognised extension directory, and %q is not one:\n%s",
					insp.NestedRoots[0], msg)
			}
			if msg == ctrlMsg {
				t.Errorf("the sentence is IDENTICAL to the one for a root that really is an "+
					"extension, so it is chosen from two counts that both trees share and "+
					"not from evidence about the directory it names:\n%s", msg)
			}

			// A branch that says nothing must not leave its separator behind: the
			// sentence before the switch ends with a space and the one after it
			// begins with one.
			if strings.Contains(msg, "  ") {
				t.Errorf("the sentence carries a double space:\n%s", msg)
			}

			// The fault it exists for must still be reported, or the repair is a
			// deletion of the message rather than of the false claim.
			if !strings.Contains(msg, "не на корень выгрузки") {
				t.Errorf("the message stopped naming the fault it exists for:\n%s", msg)
			}
			if !strings.Contains(msg, dumpReportMarker) {
				t.Errorf("the message does not name the flag to change:\n%s", msg)
			}
		})
	}
}
