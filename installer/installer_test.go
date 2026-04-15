package installer

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestBuildDesignerArgs(t *testing.T) {
	tests := []struct {
		name       string
		dbPath     string
		serverMode bool
		dbUser     string
		dbPassword string
		logPath    string
		extraArgs  []string
		want       []string
	}{
		{
			name:   "file mode without credentials",
			dbPath: `C:\MyBase`, logPath: "log.txt",
			extraArgs: []string{"/LoadConfigFromFiles", "/tmp/ext"},
			want: []string{
				"DESIGNER", "/F", `C:\MyBase`,
				"/WA-", "/DisableStartupDialogs", "/DisableStartupMessages",
				"/LoadConfigFromFiles", "/tmp/ext",
				"/Out", "log.txt",
			},
		},
		{
			name:       "file mode with credentials",
			dbPath:     `C:\MyBase`,
			dbUser:     "Admin",
			dbPassword: "pass",
			logPath:    "log.txt",
			extraArgs:  []string{"/LoadConfigFromFiles", "/tmp/ext"},
			want: []string{
				"DESIGNER", "/F", `C:\MyBase`,
				"/N", "Admin", "/P", "pass",
				"/WA-", "/DisableStartupDialogs", "/DisableStartupMessages",
				"/LoadConfigFromFiles", "/tmp/ext",
				"/Out", "log.txt",
			},
		},
		{
			name:       "server mode without credentials",
			dbPath:     `server01\accounting`,
			serverMode: true,
			logPath:    "log.txt",
			extraArgs:  []string{"/UpdateDBCfg"},
			want: []string{
				"DESIGNER", "/S", `server01\accounting`,
				"/WA-", "/DisableStartupDialogs", "/DisableStartupMessages",
				"/UpdateDBCfg",
				"/Out", "log.txt",
			},
		},
		{
			name:       "server mode with credentials",
			dbPath:     `server01\accounting`,
			serverMode: true,
			dbUser:     "Admin",
			dbPassword: "secret",
			logPath:    "log.txt",
			want: []string{
				"DESIGNER", "/S", `server01\accounting`,
				"/N", "Admin", "/P", "secret",
				"/WA-", "/DisableStartupDialogs", "/DisableStartupMessages",
				"/Out", "log.txt",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildDesignerArgs(tc.dbPath, tc.serverMode, tc.dbUser, tc.dbPassword, tc.logPath, tc.extraArgs...)
			if !slices.Equal(got, tc.want) {
				t.Errorf("mismatch\ngot:  %v\nwant: %v", got, tc.want)
			}
		})
	}
}

func TestPlatformPatterns(t *testing.T) {
	patterns := platformPatterns()
	if len(patterns) == 0 {
		t.Fatalf("expected non-empty patterns for GOOS=%s", runtime.GOOS)
	}
	t.Logf("GOOS=%s, patterns: %v", runtime.GOOS, patterns)
}

func TestExtractXMLTag(t *testing.T) {
	xml := `<Properties>
		<CompatibilityMode>Version8_3_24</CompatibilityMode>
		<InterfaceCompatibilityMode>TaxiEnableVersion8_2</InterfaceCompatibilityMode>
	</Properties>`

	if got := extractXMLTag(xml, "CompatibilityMode"); got != "Version8_3_24" {
		t.Errorf("CompatibilityMode = %q, want Version8_3_24", got)
	}
	if got := extractXMLTag(xml, "InterfaceCompatibilityMode"); got != "TaxiEnableVersion8_2" {
		t.Errorf("InterfaceCompatibilityMode = %q, want TaxiEnableVersion8_2", got)
	}
	if got := extractXMLTag(xml, "Missing"); got != "" {
		t.Errorf("Missing = %q, want empty", got)
	}
}

func TestPatchExtensionXML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Configuration.xml")

	original := `<Properties>
			<ConfigurationExtensionCompatibilityMode>Version8_3_14</ConfigurationExtensionCompatibilityMode>
			<DefaultRunMode>ManagedApplication</DefaultRunMode>
		</Properties>`

	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := patchExtensionXML(path, "Version8_3_24", "TaxiEnableVersion8_2"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	if !strings.Contains(content, "<ConfigurationExtensionCompatibilityMode>Version8_3_24</ConfigurationExtensionCompatibilityMode>") {
		t.Error("CompatibilityMode not patched")
	}
	if !strings.Contains(content, "<InterfaceCompatibilityMode>TaxiEnableVersion8_2</InterfaceCompatibilityMode>") {
		t.Error("InterfaceCompatibilityMode not inserted")
	}
}

func TestPatchExtensionXML_InsertBoth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Configuration.xml")

	original := `<Properties>
			<DefaultRunMode>ManagedApplication</DefaultRunMode>
		</Properties>`

	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := patchExtensionXML(path, "Version8_3_20", "Taxi"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	if !strings.Contains(content, "Version8_3_20") {
		t.Error("CompatibilityMode not inserted")
	}
	if !strings.Contains(content, "<InterfaceCompatibilityMode>Taxi</InterfaceCompatibilityMode>") {
		t.Error("InterfaceCompatibilityMode not inserted")
	}
}

func TestReplaceOrInsertXMLTag(t *testing.T) {
	// Replace existing tag.
	content := `<Foo>old</Foo>`
	got := replaceOrInsertXMLTag(content, "Foo", "new")
	if !strings.Contains(got, "<Foo>new</Foo>") {
		t.Errorf("replace failed: %s", got)
	}

	// Insert missing tag.
	content = `<Properties>
		</Properties>`
	got = replaceOrInsertXMLTag(content, "Bar", "val")
	if !strings.Contains(got, "<Bar>val</Bar>") {
		t.Errorf("insert failed: %s", got)
	}
}

func TestFindPlatform(t *testing.T) {
	path, err := FindPlatform()
	if err != nil {
		t.Logf("1C not installed (expected on CI): %v", err)
		return
	}
	t.Logf("Found 1C at: %s", path)
}

func TestExtractPlatformMinor(t *testing.T) {
	tests := []struct {
		path      string
		wantMajor int
		wantMinor int
		wantOK    bool
	}{
		{`C:\Program Files\1cv8\8.3.27.1859\bin\1cv8.exe`, 3, 27, true},
		{`C:\Program Files\1cv8\8.3.14.2000\bin\1cv8.exe`, 3, 14, true},
		{`/opt/1cv8/x86_64/8.3.22.1709/1cv8`, 3, 22, true},
		{`/opt/1cv8/x86_64/8.5.1.100/1cv8`, 5, 1, true},
		{`/Applications/1cv8.localized/8.3.25.1000/1cv8.app/Contents/MacOS/1cv8`, 3, 25, true},
		{`/usr/bin/some-tool`, 0, 0, false},
		{``, 0, 0, false},
	}

	for _, tc := range tests {
		major, minor, ok := extractPlatformMinor(tc.path)
		if ok != tc.wantOK || major != tc.wantMajor || minor != tc.wantMinor {
			t.Errorf("extractPlatformMinor(%q) = (%d, %d, %v), want (%d, %d, %v)",
				tc.path, major, minor, ok, tc.wantMajor, tc.wantMinor, tc.wantOK)
		}
	}
}

func TestFormatVersionForPlatform(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		// Each platform version gets its correct format.
		{`C:\Program Files\1cv8\8.3.14.2000\bin\1cv8.exe`, "2.8"},
		{`C:\Program Files\1cv8\8.3.15.2000\bin\1cv8.exe`, "2.9"},
		{`C:\Program Files\1cv8\8.3.16.2000\bin\1cv8.exe`, "2.9.1"},
		{`C:\Program Files\1cv8\8.3.17.2000\bin\1cv8.exe`, "2.10"},
		{`C:\Program Files\1cv8\8.3.18.2000\bin\1cv8.exe`, "2.11"},
		{`C:\Program Files\1cv8\8.3.19.2000\bin\1cv8.exe`, "2.12"},
		{`C:\Program Files\1cv8\8.3.20.2000\bin\1cv8.exe`, "2.13"},
		{`C:\Program Files\1cv8\8.3.21.2000\bin\1cv8.exe`, "2.14"},
		{`C:\Program Files\1cv8\8.3.22.2000\bin\1cv8.exe`, "2.15"},
		{`C:\Program Files\1cv8\8.3.23.2000\bin\1cv8.exe`, "2.16"},
		{`C:\Program Files\1cv8\8.3.24.2000\bin\1cv8.exe`, "2.17"},
		{`C:\Program Files\1cv8\8.3.25.2000\bin\1cv8.exe`, "2.18"},
		{`C:\Program Files\1cv8\8.3.26.2000\bin\1cv8.exe`, "2.19"},
		{`C:\Program Files\1cv8\8.3.27.1859\bin\1cv8.exe`, "2.20"},
		// Platform 8.5 gets format 2.21.
		{`/opt/1cv8/x86_64/8.5.1.100/1cv8`, platform85FormatVersion},
		// Unknown path falls back to default.
		{`/usr/bin/unknown`, defaultFormatVersion},
		{``, defaultFormatVersion},
	}

	for _, tc := range tests {
		got := formatVersionForPlatform(tc.path)
		if got != tc.want {
			t.Errorf("formatVersionForPlatform(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestPatchFormatVersion(t *testing.T) {
	dir := t.TempDir()

	// Create Configuration.xml with version="2.21".
	cfgXML := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.21">
	<Configuration uuid="test-uuid">
		<Properties>
			<Name>Test</Name>
		</Properties>
	</Configuration>
</MetaDataObject>`
	cfgPath := filepath.Join(dir, "Configuration.xml")
	if err := os.WriteFile(cfgPath, []byte(cfgXML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create ConfigDumpInfo.xml with version="2.21".
	dumpXML := `<?xml version="1.0" encoding="UTF-8"?>
<ConfigDumpInfo xmlns="http://v8.1c.ru/8.3/xcf/dumpinfo" format="Hierarchical" version="2.21">
	<ConfigVersions/>
</ConfigDumpInfo>`
	dumpPath := filepath.Join(dir, "ConfigDumpInfo.xml")
	if err := os.WriteFile(dumpPath, []byte(dumpXML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a subdirectory with another XML file.
	subDir := filepath.Join(dir, "HTTPServices")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	svcXML := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.21">
	<HTTPService uuid="svc-uuid"/>
</MetaDataObject>`
	svcPath := filepath.Join(subDir, "MCPService.xml")
	if err := os.WriteFile(svcPath, []byte(svcXML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a non-XML file that should NOT be patched.
	txtPath := filepath.Join(dir, "readme.txt")
	txtContent := `version="2.21" should not be patched`
	if err := os.WriteFile(txtPath, []byte(txtContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Patch to version 2.18.
	if err := patchFormatVersion(dir, "2.18"); err != nil {
		t.Fatal(err)
	}

	// Verify Configuration.xml was patched.
	data, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(data), `version="2.18"`) {
		t.Errorf("Configuration.xml not patched:\n%s", data)
	}
	if strings.Contains(string(data), `version="2.21"`) {
		t.Errorf("Configuration.xml still contains old version:\n%s", data)
	}
	// Verify XML declaration was NOT touched.
	if !strings.Contains(string(data), `<?xml version="1.0"`) {
		t.Errorf("XML declaration version was incorrectly modified:\n%s", data)
	}

	// Verify ConfigDumpInfo.xml was patched.
	data, _ = os.ReadFile(dumpPath)
	if !strings.Contains(string(data), `version="2.18"`) {
		t.Errorf("ConfigDumpInfo.xml not patched:\n%s", data)
	}

	// Verify subdirectory XML was patched.
	data, _ = os.ReadFile(svcPath)
	if !strings.Contains(string(data), `version="2.18"`) {
		t.Errorf("HTTPServices/MCPService.xml not patched:\n%s", data)
	}

	// Verify non-XML file was NOT patched.
	data, _ = os.ReadFile(txtPath)
	if string(data) != txtContent {
		t.Errorf("non-XML file was modified: %s", data)
	}
}

func TestPatchFormatVersion_NoVersionAttr(t *testing.T) {
	dir := t.TempDir()

	// XML file without version= attribute should not be modified.
	original := `<?xml version="1.0" encoding="UTF-8"?>
<Root><Child>value</Child></Root>`
	path := filepath.Join(dir, "test.xml")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := patchFormatVersion(dir, "2.16"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Errorf("file without version attr was modified:\n%s", data)
	}
}

func TestPatchFormatVersion_AlreadyCorrect(t *testing.T) {
	dir := t.TempDir()

	// File already has the target version - should not be rewritten.
	original := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject version="2.16"><Data/></MetaDataObject>`
	path := filepath.Join(dir, "test.xml")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := patchFormatVersion(dir, "2.16"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Errorf("file with matching version was modified:\n%s", data)
	}
}

func TestStripUnsupportedElements(t *testing.T) {
	dir := t.TempDir()

	// Configuration.xml with KeepMappingToExtendedConfigurationObjectsByIDs
	// and InternalInfo with ContainedObject entries including the Role ClassId
	// (fb282519...) that platform 8.3.13 does not recognize.
	cfgXML := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.10">
	<Configuration uuid="test-uuid">
		<InternalInfo>
			<xr:ContainedObject>
				<xr:ClassId>9cd510cd-abfc-11d4-9434-004095e12fc7</xr:ClassId>
				<xr:ObjectId>225193c1-ba53-407d-b373-f46f8b16de81</xr:ObjectId>
			</xr:ContainedObject>
			<xr:ContainedObject>
				<xr:ClassId>fb282519-d103-4dd3-bc12-cb271d631dfc</xr:ClassId>
				<xr:ObjectId>20301740-cf02-4bee-bfa3-5f679eaf9ae0</xr:ObjectId>
			</xr:ContainedObject>
		</InternalInfo>
		<Properties>
			<Name>TestExt</Name>
			<KeepMappingToExtendedConfigurationObjectsByIDs>true</KeepMappingToExtendedConfigurationObjectsByIDs>
			<NamePrefix>Test_</NamePrefix>
		</Properties>
	</Configuration>
</MetaDataObject>`
	cfgPath := filepath.Join(dir, "Configuration.xml")
	if err := os.WriteFile(cfgPath, []byte(cfgXML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Language XML with empty InternalInfo (self-closing tag).
	langDir := filepath.Join(dir, "Languages")
	if err := os.MkdirAll(langDir, 0o755); err != nil {
		t.Fatal(err)
	}
	langXML := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.10">
	<Language uuid="74817ceb-test">
		<InternalInfo/>
		<Properties>
			<Name>Russian</Name>
		</Properties>
	</Language>
</MetaDataObject>`
	langPath := filepath.Join(langDir, "Russian.xml")
	if err := os.WriteFile(langPath, []byte(langXML), 0o644); err != nil {
		t.Fatal(err)
	}

	// XML file without InternalInfo or KeepMapping (should remain unchanged).
	svcXML := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.10">
	<HTTPService uuid="svc-uuid">
		<Properties><Name>TestService</Name></Properties>
	</HTTPService>
</MetaDataObject>`
	svcPath := filepath.Join(dir, "Service.xml")
	if err := os.WriteFile(svcPath, []byte(svcXML), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := stripUnsupportedElements(dir); err != nil {
		t.Fatal(err)
	}

	// Verify Configuration.xml: KeepMapping removed.
	data, _ := os.ReadFile(cfgPath)
	content := string(data)
	if strings.Contains(content, "KeepMappingToExtendedConfigurationObjectsByIDs") {
		t.Error("KeepMappingToExtendedConfigurationObjectsByIDs was not removed from Configuration.xml")
	}
	// Verify Configuration.xml: InternalInfo is preserved (contains UUID mapping).
	if !strings.Contains(content, "<InternalInfo>") {
		t.Error("InternalInfo section was incorrectly removed from Configuration.xml")
	}
	// Verify Role ContainedObject (ClassId fb282519...) was stripped.
	if strings.Contains(content, "fb282519-d103-4dd3-bc12-cb271d631dfc") {
		t.Error("Role ContainedObject (ClassId fb282519...) was not removed from Configuration.xml")
	}
	// Verify Language ContainedObject (ClassId 9cd510cd...) is preserved.
	if !strings.Contains(content, "9cd510cd-abfc-11d4-9434-004095e12fc7") {
		t.Error("Language ContainedObject was incorrectly removed from Configuration.xml")
	}
	// Verify other properties remain intact.
	if !strings.Contains(content, "<Name>TestExt</Name>") {
		t.Error("Name element was incorrectly removed from Configuration.xml")
	}
	if !strings.Contains(content, "<NamePrefix>Test_</NamePrefix>") {
		t.Error("NamePrefix element was incorrectly removed from Configuration.xml")
	}

	// Verify Language XML: empty InternalInfo removed.
	data, _ = os.ReadFile(langPath)
	content = string(data)
	if strings.Contains(content, "InternalInfo") {
		t.Error("InternalInfo was not removed from Language XML")
	}
	if !strings.Contains(content, "<Name>Russian</Name>") {
		t.Error("Name element was incorrectly removed from Language XML")
	}

	// Verify unrelated XML file was not modified.
	data, _ = os.ReadFile(svcPath)
	if string(data) != svcXML {
		t.Error("Service.xml was unexpectedly modified")
	}
}

func TestStripUnsupportedElements_NoChanges(t *testing.T) {
	dir := t.TempDir()

	// Configuration.xml without KeepMapping or InternalInfo.
	cfgXML := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject version="2.10">
	<Configuration uuid="test">
		<Properties>
			<Name>Clean</Name>
		</Properties>
	</Configuration>
</MetaDataObject>`
	cfgPath := filepath.Join(dir, "Configuration.xml")
	if err := os.WriteFile(cfgPath, []byte(cfgXML), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := stripUnsupportedElements(dir); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(cfgPath)
	if string(data) != cfgXML {
		t.Errorf("file without unsupported elements was modified:\n%s", data)
	}
}

func TestStripInheritedProperties(t *testing.T) {
	type check struct {
		text   string
		present bool
	}

	tests := []struct {
		name   string
		input  string
		checks []check
	}{
		{
			name: "strips all 12 inherited elements and preserves others",
			input: `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.10">
	<Configuration uuid="test-uuid">
		<Properties>
			<Name>TestExt</Name>
			<NamePrefix>Test_</NamePrefix>
			<ConfigurationExtensionCompatibilityMode>Version8_3_13</ConfigurationExtensionCompatibilityMode>
			<DefaultRoles>
				<xr:Item>Role1</xr:Item>
			</DefaultRoles>
			<DefaultRunMode>ManagedApplication</DefaultRunMode>
			<UsePurposes>
				<xr:Item>PersonalComputer</xr:Item>
			</UsePurposes>
			<ScriptVariant>English</ScriptVariant>
			<Vendor>TestVendor</Vendor>
			<Version>1.0.0</Version>
			<DefaultLanguage>Language.Russian</DefaultLanguage>
			<BriefInformation>
				<xr:item lang="ru">Brief info</xr:item>
			</BriefInformation>
			<DetailedInformation>
				<xr:item lang="ru">Detailed info</xr:item>
			</DetailedInformation>
			<Copyright>
				<xr:item lang="ru">Copyright text</xr:item>
			</Copyright>
			<VendorInformationAddress>
				<xr:item lang="ru">http://vendor.example</xr:item>
			</VendorInformationAddress>
			<ConfigurationInformationAddress>
				<xr:item lang="ru">http://config.example</xr:item>
			</ConfigurationInformationAddress>
		</Properties>
	</Configuration>
</MetaDataObject>`,
			checks: []check{
				// Stripped elements.
				{"<DefaultRoles>", false},
				{"<DefaultRunMode>", false},
				{"<UsePurposes>", false},
				{"<ScriptVariant>", false},
				{"<Vendor>", false},
				{"<Version>1.0.0</Version>", false},
				{"<DefaultLanguage>", false},
				{"<BriefInformation>", false},
				{"<DetailedInformation>", false},
				{"<Copyright>", false},
				{"<VendorInformationAddress>", false},
				{"<ConfigurationInformationAddress>", false},
				// Preserved elements.
				{"<Name>TestExt</Name>", true},
				{"<NamePrefix>Test_</NamePrefix>", true},
				{"<ConfigurationExtensionCompatibilityMode>Version8_3_13</ConfigurationExtensionCompatibilityMode>", true},
				{"<Properties>", true},
				{"</Properties>", true},
				{`<?xml version="1.0"`, true},
			},
		},
		{
			name: "no inherited elements leaves file unchanged",
			input: `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject version="2.10">
	<Configuration uuid="clean">
		<Properties>
			<Name>Clean</Name>
		</Properties>
	</Configuration>
</MetaDataObject>`,
			checks: []check{
				{"<Name>Clean</Name>", true},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "Configuration.xml")
			if err := os.WriteFile(path, []byte(tc.input), 0o644); err != nil {
				t.Fatal(err)
			}

			if err := stripInheritedProperties(path); err != nil {
				t.Fatal(err)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			content := string(data)

			for _, c := range tc.checks {
				found := strings.Contains(content, c.text)
				if c.present && !found {
					t.Errorf("expected %q to be present, but it was not.\nContent:\n%s", c.text, content)
				}
				if !c.present && found {
					t.Errorf("expected %q to be stripped, but it is still present.\nContent:\n%s", c.text, content)
				}
			}
		})
	}
}

func TestParsePlatformVersion(t *testing.T) {
	tests := []struct {
		name        string
		platformExe string
		override    string
		wantMajor   int
		wantMinor   int
	}{
		{"override takes priority", "/path/8.3.13.1644/bin/1cv8", "8.5.1", 5, 1},
		{"from exe path", "/Applications/1cv8t.localized/8.3.13.1644/1cv8t.app/Contents/MacOS/1cv8t", "", 3, 13},
		{"windows path", `C:\Program Files\1cv8\8.3.27.1989\bin\1cv8.exe`, "", 3, 27},
		{"8.5 path", `C:\Program Files\1cv8t\8.5.1.1150\bin\1cv8t.exe`, "", 5, 1},
		{"both empty", "", "", 0, 0},
		{"override only", "", "8.3.14", 3, 14},
		{"override with build", "", "8.3.14.1234", 3, 14},
		{"no version in path", "/usr/local/bin/1cv8", "", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			major, minor := parsePlatformVersion(tt.platformExe, tt.override)
			if major != tt.wantMajor || minor != tt.wantMinor {
				t.Errorf("parsePlatformVersion(%q, %q) = (%d, %d), want (%d, %d)",
					tt.platformExe, tt.override, major, minor, tt.wantMajor, tt.wantMinor)
			}
		})
	}
}

func TestPlatformOlderThan(t *testing.T) {
	tests := []struct {
		name                                 string
		major, minor, targetMajor, targetMinor int
		want                                 bool
	}{
		{"same version", 3, 14, 3, 14, false},
		{"older minor", 3, 13, 3, 14, true},
		{"newer minor", 3, 15, 3, 14, false},
		{"8.5 vs 8.3.14", 5, 1, 3, 14, false},
		{"8.3 vs 8.5.0", 3, 27, 5, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := platformOlderThan(tt.major, tt.minor, tt.targetMajor, tt.targetMinor)
			if got != tt.want {
				t.Errorf("platformOlderThan(%d, %d, %d, %d) = %v, want %v",
					tt.major, tt.minor, tt.targetMajor, tt.targetMinor, got, tt.want)
			}
		})
	}
}
