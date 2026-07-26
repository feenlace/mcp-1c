package extension

import "embed"

// The extension ships no Languages/ object and no <DefaultLanguage>. It used to
// declare an ADOPTED Language.Русский, which asserts the extended configuration
// already has a language of that name. On 1С 8.3.27 an infobase created by
// `ibcmd infobase create` comes out with Language.English only, independent of
// locale, so the load aborted there on a controlled-property mismatch for
// DefaultLanguage followed by "Object not found: Language.Русский". Per-object
// <Synonym> items keep their <v8:lang>ru</v8:lang> entries: a synonym is keyed
// by language CODE and resolves against the languages of the MERGED
// configuration, which the base supplies.
//
//go:embed src/ConfigDumpInfo.xml
//go:embed src/Configuration.xml
//go:embed src/Roles/MCP_ОсновнаяРоль.xml
//go:embed src/Roles/MCP_ОсновнаяРоль/Ext/Rights.xml
//go:embed src/HTTPServices/MCPService.xml
//go:embed src/HTTPServices/MCPService/Ext/Module.bsl
var Source embed.FS
