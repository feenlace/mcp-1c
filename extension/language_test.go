package extension

import (
	"encoding/xml"
	"io/fs"
	"strings"
	"testing"
)

// This file pins the language-agnostic shape of the shipped extension tree.
//
// What went wrong: Configuration.xml declared
//
//	<DefaultLanguage>Language.Русский</DefaultLanguage>
//	<ChildObjects><Language>Русский</Language>
//
// with Languages/Русский.xml carrying <ObjectBelonging>Adopted</ObjectBelonging>,
// which asserts the extended configuration already has a language of that name
// and that the extension merely borrows it.
//
// On 1С 8.3.27 an infobase created by `ibcmd infobase create` comes out with
// <DefaultLanguage>Language.English</DefaultLanguage> and only
// Languages/English.xml, independent of locale. The assertion is therefore
// false on such a base, and installing the extension aborts with exit code 1:
//
//	Value of DefaultLanguage property of  object with value check enabled
//	does not match the value in the extended configuration
//	Object not found: Language.Русский
//
// after which the extension is left registered and active with an all-zero
// hash-sum, i.e. imported but never applied.
//
// The extension does not use the declared language. No BSL reads it, and the
// service module deliberately excludes Языки from the metadata universe it
// enumerates. Per-object <Synonym> items keep their <v8:lang>ru</v8:lang>
// entries: a synonym is keyed by language CODE and resolves against the
// languages of the MERGED configuration, which the base supplies.
//
// These assertions are about what the extension DECLARES. They cannot execute
// the 1С platform. The definitive proof is a real install into an
// English-language base.

const languageConfigurationPath = "src/Configuration.xml"

// configurationLanguageView is the minimal projection of Configuration.xml
// needed here: the root identity plus the ordered ChildObjects list. Each
// ChildObject is decoded generically, so the parser needs no per-type table.
type configurationLanguageView struct {
	Name         string `xml:"Configuration>Properties>Name"`
	ChildObjects struct {
		Items []struct {
			XMLName xml.Name
			Value   string `xml:",chardata"`
		} `xml:",any"`
	} `xml:"Configuration>ChildObjects"`
}

// TestExtensionDeclaresNoDefaultLanguage asserts the shipped Configuration.xml
// carries no <DefaultLanguage> property. That property is value-checked against
// the extended configuration, so any value at all restricts the extension to
// bases whose default language matches it.
func TestExtensionDeclaresNoDefaultLanguage(t *testing.T) {
	data, err := Source.ReadFile(languageConfigurationPath)
	if err != nil {
		t.Fatalf("reading embedded %s: %v", languageConfigurationPath, err)
	}
	// Vacuity guard: prove these bytes are the document we think they are
	// before asserting an absence over them.
	if !strings.Contains(string(data), "<ConfigurationExtensionPurpose>") {
		t.Fatalf("embedded %s does not look like an extension descriptor", languageConfigurationPath)
	}
	if strings.Contains(string(data), "<DefaultLanguage") {
		t.Error("Configuration.xml declares <DefaultLanguage>; it is a value-checked property, " +
			"so the extension will refuse to install into a base with a different default language")
	}
}

// TestExtensionDeclaresNoLanguageObject asserts the shipped Configuration.xml
// registers no <Language> ChildObject. Ours was ObjectBelonging=Adopted, which
// claims the object already exists in the extended configuration; on a base
// without Language.Русский the load fails with "Object not found".
func TestExtensionDeclaresNoLanguageObject(t *testing.T) {
	data, err := Source.ReadFile(languageConfigurationPath)
	if err != nil {
		t.Fatalf("reading embedded %s: %v", languageConfigurationPath, err)
	}
	var doc configurationLanguageView
	if err := xml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", languageConfigurationPath, err)
	}
	// Vacuity guard: a projection that extracted nothing would make the loop
	// below pass without checking anything.
	if strings.TrimSpace(doc.Name) == "" || len(doc.ChildObjects.Items) == 0 {
		t.Fatalf("projection extracted nothing (name=%q, %d ChildObjects)", doc.Name, len(doc.ChildObjects.Items))
	}
	for _, item := range doc.ChildObjects.Items {
		if item.XMLName.Local == "Language" {
			t.Errorf("Configuration.xml registers ChildObject <Language>%s</Language>; "+
				"an adopted language object cannot resolve on a base that does not have it",
				strings.TrimSpace(item.Value))
		}
	}
}

// TestExtensionEmbedsNoLanguageDescriptor asserts no Languages/*.xml descriptor
// is embedded. The ChildObjects list and the descriptor set on disk must agree:
// a descriptor that is not registered is read by Configurator as a foreign
// object and rejected.
func TestExtensionEmbedsNoLanguageDescriptor(t *testing.T) {
	var walked int
	var offenders []string
	err := fs.WalkDir(Source, "src", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		walked++
		if strings.Contains(p, "/Languages/") {
			offenders = append(offenders, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking embed: %v", err)
	}
	if walked == 0 {
		t.Fatal("walked zero embedded files under src")
	}
	if len(offenders) > 0 {
		t.Errorf("embeds language descriptor(s) %v; the extension must not ship a Language object", offenders)
	}
}

// TestConfigDumpInfoListsNoLanguage asserts the shipped dump manifest no longer
// advertises a Language object. This tree does ship ConfigDumpInfo.xml, and the
// manifest must describe the object set that is actually present.
func TestConfigDumpInfoListsNoLanguage(t *testing.T) {
	const manifestPath = "src/ConfigDumpInfo.xml"
	data, err := Source.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading embedded %s: %v", manifestPath, err)
	}
	if !strings.Contains(string(data), "<ConfigVersions>") {
		t.Fatalf("embedded %s does not look like a dump manifest", manifestPath)
	}
	if strings.Contains(string(data), `name="Language.`) {
		t.Error("ConfigDumpInfo.xml still lists a Language metadata entry; " +
			"it must match the shipped object set")
	}
}
