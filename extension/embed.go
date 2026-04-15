package extension

import "embed"

//go:embed src/ConfigDumpInfo.xml
//go:embed src/Configuration.xml
//go:embed src/Languages/Русский.xml
//go:embed src/Roles/MCP_ОсновнаяРоль.xml
//go:embed src/Roles/MCP_ОсновнаяРоль/Ext/Rights.xml
//go:embed src/HTTPServices/MCPService.xml
//go:embed src/HTTPServices/MCPService/Ext/Module.bsl
var Source embed.FS
