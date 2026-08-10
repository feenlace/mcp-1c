package extension

import (
	"encoding/xml"
	"slices"
	"testing"
)

// ---------------------------------------------------------------------------
// The extension MUST declare MCP_ОсновнаяРоль as a default role, and this file
// exists because the opposite was argued convincingly and was wrong.
//
// The argument for removing it: a paying customer on a Standard Subsystems
// configuration cannot start a session, because the library reads the effective
// default-role list, finds our extra entry and aborts with «в свойстве
// ОсновныеРоли не указаны стандартные роли ... или указаны лишние роли». 1C's
// documentation says default roles apply only «если список пользователей
// прикладного решения пуст», so on a base with users the declaration looked
// like dead weight that only caused harm.
//
// MEASURED on a Windows VM instead, two file bases identical except for this
// one element, five users each, GET /hs/mcp-1c/version:
//
//	user holding ПолныеПрава        WITH 200 -> WITHOUT 403
//	user holding ОбычныйДоступ      WITH 200 -> WITHOUT 403
//	user holding MCP_ОсновнаяРоль   WITH 200 -> WITHOUT 200
//	user holding no roles at all    WITH 403 -> WITHOUT 403
//	anonymous                       WITH 401 -> WITHOUT 401
//
// Causation was confirmed bidirectionally on the same bases: adding the element
// flipped 403 back to 200 and removing it flipped 200 to 403, while the users
// holding our own role were unaffected either way.
//
// So the declaration is what grants the service to every user who holds any
// role other than ours, which in a working base is every working user. The
// documentation does not describe what the platform does here. Note also why the
// wrong argument was persuasive: the user for whom the element truly does
// nothing is the user with no roles at all, and that user was already denied.
//
// The customer's symptom is real and is addressed at install time instead, by
// the --strip-default-roles flag in the installer, which removes this element
// from the copy it loads. The default ships the element.
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
			// empty" stay distinguishable: an empty <DefaultRoles/> is still a
			// declaration, and it grants nothing.
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

// configurationWithoutDefaultRoles is the shipped file's Properties block with
// the declaration taken out. It is the negative control: the reader must report
// this one as undeclared, or its verdict on the shipped file means nothing.
const configurationWithoutDefaultRoles = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses"
	xmlns:xr="http://v8.1c.ru/8.3/xcf/readable"
	xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" version="2.10">
	<Configuration uuid="control">
		<Properties>
			<Name>MCP_HTTPService</Name>
			<DefaultRunMode>ManagedApplication</DefaultRunMode>
		</Properties>
		<ChildObjects>
			<Role>MCP_ОсновнаяРоль</Role>
		</ChildObjects>
	</Configuration>
</MetaDataObject>`

func TestConfigurationDeclaresTheRoleAsADefaultRole(t *testing.T) {
	raw, err := Source.ReadFile(configurationPath)
	if err != nil {
		t.Fatalf("read %s: %v", configurationPath, err)
	}

	// Negative control first: the same reader over XML that does NOT declare
	// default roles must say so. Without it, a struct that stopped matching the
	// document would report every input as declared.
	control := parseConfiguration(t, []byte(configurationWithoutDefaultRoles))
	if control.Configuration.Properties.DefaultRoles != nil {
		t.Fatal("negative control: the reader reports <DefaultRoles> in XML that has none, " +
			"so its verdict on the shipped file means nothing")
	}
	if !slices.Contains(control.Configuration.ChildObjects.Roles, "MCP_ОсновнаяРоль") {
		t.Fatal("negative control: the reader cannot see ChildObjects roles at all")
	}

	doc := parseConfiguration(t, raw)

	declared := doc.Configuration.Properties.DefaultRoles
	if declared == nil {
		t.Fatalf("%s no longer declares <DefaultRoles>. Measured on two bases differing only in this "+
			"element: without it a user holding ПолныеПрава or ОбычныйДоступ gets 403 from "+
			"/hs/mcp-1c/version, with it 200. Removing it takes the service away from every user who "+
			"does not hold MCP_ОсновнаяРоль explicitly. Customers whose Standard Subsystems "+
			"configuration rejects the extra entry use the installer flag --strip-default-roles instead",
			configurationPath)
	}
	if !slices.Contains(declared.Items, "Role.MCP_ОсновнаяРоль") {
		t.Errorf("%s declares default roles %v, which does not include Role.MCP_ОсновнаяРоль. "+
			"An empty or foreign declaration grants nothing", configurationPath, declared.Items)
	}

	// The role itself must exist, or the declaration names an object the .cfe
	// does not contain.
	if !slices.Contains(doc.Configuration.ChildObjects.Roles, "MCP_ОсновнаяРоль") {
		t.Errorf("%s no longer lists MCP_ОсновнаяРоль in <ChildObjects>, got %v",
			configurationPath, doc.Configuration.ChildObjects.Roles)
	}

	// The role's own source files must still be shipped.
	for _, path := range []string{
		"src/Roles/MCP_ОсновнаяРоль.xml",
		"src/Roles/MCP_ОсновнаяРоль/Ext/Rights.xml",
	} {
		if _, readErr := Source.ReadFile(path); readErr != nil {
			t.Errorf("embedded extension source is missing %s: %v", path, readErr)
		}
	}
}
