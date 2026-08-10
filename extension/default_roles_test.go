package extension

import (
	"bytes"
	"encoding/xml"
	"slices"
	"testing"
)

// ---------------------------------------------------------------------------
// The extension must NOT name itself a default role of the configuration.
//
// Reported by a paying customer: on a configuration built on the Standard
// Subsystems Library, session start aborts inside ПередЗапускомПрограммы with
// «В конфигурации в свойстве ОсновныеРоли не указаны стандартные роли
// АдминистраторСистемы и ПолныеПрава или указаны лишние роли». The library reads
// the effective default-role list, finds MCP_ОсновнаяРоль in it and treats the
// extra entry as a misconfigured application. The only way out for the customer
// was to clear the extension's default roles by hand after EVERY update.
//
// The declaration bought nothing in exchange. Default roles are handed out by
// the platform only «если список пользователей прикладного решения пуст», and
// the extension never sets UseDefaultRolesForAllUsers, so on a base that has
// users the declaration does not grant anybody anything. The mechanism that
// does work is the role held by the user: commit 12262df0 fixed 403 for
// least-privilege users by adding a Use grant to Rights.xml, not by touching
// default roles.
//
// Removing the declaration does not remove the role. Its existence is declared
// separately, in ChildObjects, and both halves are read here so a fix that
// deleted the role along with the declaration reddens instead of shipping.
//
// Both readings below are paired with a positive control on synthetic XML of
// the same shape, because "the element is absent" and "the reader never looks at
// the element" produce the same green.
// ---------------------------------------------------------------------------

const configurationPath = "src/Configuration.xml"

// configurationDoc mirrors the parts of extension/src/Configuration.xml this
// guard reads. Element names are matched by local name, so neither the default
// XML namespace on the root nor the xr: prefix on Item matters here.
type configurationDoc struct {
	XMLName       xml.Name `xml:"MetaDataObject"`
	Configuration struct {
		Properties struct {
			Name string `xml:"Name"`
			// A pointer so that "element absent" and "element present but
			// empty" stay distinguishable: 1С writes an empty declaration as
			// <DefaultRoles/> and that is still a declaration.
			DefaultRoles *struct {
				Items []string `xml:"Item"`
			} `xml:"DefaultRoles"`
		} `xml:"Properties"`
		ChildObjects struct {
			Roles []string `xml:"Role"`
		} `xml:"ChildObjects"`
	} `xml:"Configuration"`
}

// parseConfiguration decodes Configuration.xml into configurationDoc.
func parseConfiguration(t *testing.T, raw []byte) configurationDoc {
	t.Helper()
	var doc configurationDoc
	if err := xml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse Configuration.xml: %v", err)
	}
	return doc
}

// configurationWithDefaultRoles is the shape this guard is written against: the
// declaration exactly as it stood in the shipped extension up to 0.4.7. It is
// the positive control for both readings below.
const configurationWithDefaultRoles = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses"
	xmlns:xr="http://v8.1c.ru/8.3/xcf/readable"
	xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" version="2.10">
	<Configuration uuid="control">
		<Properties>
			<Name>MCP_HTTPService</Name>
			<DefaultRunMode>ManagedApplication</DefaultRunMode>
			<DefaultRoles>
				<xr:Item xsi:type="xr:MDObjectRef">Role.MCP_ОсновнаяРоль</xr:Item>
			</DefaultRoles>
		</Properties>
		<ChildObjects>
			<Role>MCP_ОсновнаяРоль</Role>
		</ChildObjects>
	</Configuration>
</MetaDataObject>`

func TestConfigurationDeclaresNoDefaultRoles(t *testing.T) {
	raw, err := Source.ReadFile(configurationPath)
	if err != nil {
		t.Fatalf("read %s: %v", configurationPath, err)
	}

	// Positive control first: the same reader, run over XML that DOES carry the
	// declaration, must see it. Without this, a struct that no longer matches
	// the document would report a clean absence for every input.
	control := parseConfiguration(t, []byte(configurationWithDefaultRoles))
	if control.Configuration.Properties.DefaultRoles == nil {
		t.Fatal("positive control: the reader did not see <DefaultRoles> in XML that declares it, " +
			"so its verdict on the shipped file means nothing")
	}
	if !slices.Contains(control.Configuration.Properties.DefaultRoles.Items, "Role.MCP_ОсновнаяРоль") {
		t.Fatalf("positive control: reader saw <DefaultRoles> but not its item, got %v",
			control.Configuration.Properties.DefaultRoles.Items)
	}
	if !bytes.Contains([]byte(configurationWithDefaultRoles), []byte("DefaultRoles")) {
		t.Fatal("positive control: the raw scan cannot find DefaultRoles in text that spells it out")
	}

	doc := parseConfiguration(t, raw)

	if got := doc.Configuration.Properties.DefaultRoles; got != nil {
		t.Errorf("%s declares <DefaultRoles> %v. On a Standard Subsystems Library configuration "+
			"this aborts session start in ПередЗапускомПрограммы with «в свойстве ОсновныеРоли ... "+
			"указаны лишние роли», and it grants nothing in exchange: default roles apply only to a "+
			"base with an empty user list, which no customer base is",
			configurationPath, got.Items)
	}

	// Second, independent reading. The struct above only looks inside
	// Properties; a declaration reinstated anywhere else in the file would slip
	// past it. The raw scan has no such blind spot and its control fired above.
	if bytes.Contains(raw, []byte("DefaultRoles")) {
		t.Errorf("%s still mentions DefaultRoles somewhere outside <Properties>", configurationPath)
	}

	// The role itself must survive the removal: it is what actually grants
	// access, and Rights.xml is guarded separately by rights_test.go.
	if !slices.Contains(doc.Configuration.ChildObjects.Roles, "MCP_ОсновнаяРоль") {
		t.Errorf("%s no longer lists MCP_ОсновнаяРоль in <ChildObjects>, got %v. Removing the "+
			"default-role declaration must not remove the role it named",
			configurationPath, doc.Configuration.ChildObjects.Roles)
	}

	// The role's own source files must still be shipped, or ChildObjects names
	// an object the .cfe does not contain.
	for _, path := range []string{
		"src/Roles/MCP_ОсновнаяРоль.xml",
		"src/Roles/MCP_ОсновнаяРоль/Ext/Rights.xml",
	} {
		if _, readErr := Source.ReadFile(path); readErr != nil {
			t.Errorf("embedded extension source is missing %s: %v", path, readErr)
		}
	}
}
