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
// MEASURED TWICE, and the two readings do not agree. Both are written down here
// because the disagreement is the point: whoever simplifies this table back to
// one row will restate a claim that was already shipped and already refuted.
//
// First, on a Windows VM, two SYNTHETIC file bases identical except for this one
// element, five users each, GET /hs/mcp-1c/version:
//
//	user holding ПолныеПрава        WITH 200 -> WITHOUT 403
//	user holding ОбычныйДоступ      WITH 200 -> WITHOUT 403
//	user holding MCP_ОсновнаяРоль   WITH 200 -> WITHOUT 200
//	user holding no roles at all    WITH 403 -> WITHOUT 403
//	anonymous                       WITH 401 -> WITHOUT 401
//
// Second, on a REAL typical configuration, БухгалтерияПредприятияУчебная
// 3.0.111.25, nine real users, published and called over HTTP, the element added
// and removed five times in a row, each arm proved by a dump read back off the
// base:
//
//	директор: ПолныеПрава + АдминистраторСистемы, no MCP role  200 -> 200
//	Demo: 198 ordinary БП roles, no full rights, no MCP role   200 -> 403
//	user holding MCP_ОсновнаяРоль only                         200 -> 200
//	user holding no roles at all                               403 -> 403
//	anonymous / wrong password / nonexistent user              401 throughout
//
// THE ADMINISTRATOR ROW DOES NOT GENERALISE. On the synthetic bases that user
// dropped to 403; on the real configuration the administrator keeps 200 and
// would notice nothing. The synthetic bases were misleading in a second way as
// well: their MAIN configuration declares an empty <DefaultRoles/>, and the
// merge does not happen into an empty declaration, so the effective list read as
// zero roles there. On the real base the merge is plain, three roles become four.
//
// WHAT SURVIVES BOTH READINGS, and is why this element must stay: an ordinary
// least-privileged account that holds roles of the configuration is served WITH
// the declaration and refused WITHOUT it. That is precisely the account a careful
// customer points the connector at, so removing the element by default would
// take the service away from the customers who configured it properly.
//
// NOT ATTRIBUTABLE: whether the administrator's retained access comes from
// ПолныеПрава, from АдминистраторСистемы, or from the pair. A user holding
// exactly one of them could not be constructed, so the row says "administrator"
// and stops there. Do not refine it without a base that can carry the case.
//
// COUNTER-INTUITIVE, and worth knowing before diagnosing this by hand:
// РольДоступна("MCP_ОсновнаяРоль") returns Нет for Demo even in the arm where
// Demo is served. The declaration confers the effective right without making the
// role visible to that check, so a customer who reaches for РольДоступна will be
// told the role is absent while it is working.
//
// The customer's symptom is real and is addressed at install time instead, by
// the --strip-default-roles flag in the installer, which removes this element
// from the copy it loads. The default ships the element.
//
// WHY THE ABORT DOES NOT REPRODUCE HERE, read out of the dumped source of our
// own base rather than inferred. ОбщийМодуль.СтандартныеПодсистемыСервер raises
// only when ОсновныеРоли is missing АдминистраторСистемы or ПолныеПрава. There
// is no count test, and the message ENDS at «...АдминистраторСистемы и
// ПолныеПрава.» A search for «лишние роли» across six dumped modules returned
// zero, with positive controls firing. Measured live in that base: four default
// roles including ours, both standard roles present, the expression evaluates
// false, and the early-return guard did NOT fire, so the check ran and was
// simply indifferent to the extra entry. That library is 3.1.5.331, established
// three independent ways.
//
// The customer is on an older library, and TWO INDEPENDENT FINGERPRINTS agree.
// The message: their tail reads «...ПолныеПрава или указаны лишние роли», a
// clause our source cannot produce. The stack, which does not depend on wording
// at all: their raising line in СтандартныеПодсистемыСервер is 2801 against our
// 2967, the line calling ПередЗапускомПрограммы() is 36 against our 57, and the
// line in МодульСеанса is 8 against our 16.
//
// The pre-3.1 source carrying the count rule was read from a public mirror, not
// from a base we control, so it is strong corroboration rather than something we
// measured. What stands without it, and is what the customer-facing text says:
// the flag removes OUR contribution to ОсновныеРоли; on a library whose check
// counts that list this can return the count to an accepted value PROVIDED our
// role is the only extra one; and on a library like ours, which tests only
// membership, our entry cannot cause this abort at all, so the flag has nothing
// to fix there and using it would trade working access for nothing.
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
		t.Fatalf("%s no longer declares <DefaultRoles>. The reading that holds on BOTH bases measured: "+
			"an ordinary least-privileged account holding roles of the configuration answers 200 with "+
			"this element and 403 without it. On БухгалтерияПредприятияУчебная 3.0.111.25 that was "+
			"Demo, 198 roles and no full rights, across five flips of the element. Removing it by "+
			"default takes the service away from exactly the restricted account a careful customer "+
			"points the connector at. An administrator would keep access and notice nothing, which is "+
			"why this is easy to remove and hard to catch. Customers whose Standard Subsystems "+
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
