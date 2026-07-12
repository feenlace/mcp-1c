package extension

import (
	"encoding/xml"
	"testing"
)

// Paths inside the embedded Source FS (see embed.go).
const (
	rightsPath     = "src/Roles/MCP_ОсновнаяРоль/Ext/Rights.xml"
	mcpServicePath = "src/HTTPServices/MCPService.xml"
)

// rightsDoc mirrors extension/src/Roles/MCP_ОсновнаяРоль/Ext/Rights.xml.
// Element names are matched by local name, so the default XML namespace
// declared on the root is irrelevant here.
type rightsDoc struct {
	XMLName xml.Name `xml:"Rights"`
	Objects []struct {
		Name   string `xml:"name"`
		Rights []struct {
			Name  string `xml:"name"`
			Value string `xml:"value"`
		} `xml:"right"`
	} `xml:"object"`
}

// mcpServiceDoc mirrors the parts of MCPService.xml we need: the service name
// plus every URLTemplate and its child Method names.
type mcpServiceDoc struct {
	XMLName     xml.Name `xml:"MetaDataObject"`
	HTTPService struct {
		Properties struct {
			Name string `xml:"Name"`
		} `xml:"Properties"`
		ChildObjects struct {
			URLTemplates []struct {
				Properties struct {
					Name string `xml:"Name"`
				} `xml:"Properties"`
				ChildObjects struct {
					Methods []struct {
						Properties struct {
							Name string `xml:"Name"`
						} `xml:"Properties"`
					} `xml:"Method"`
				} `xml:"ChildObjects"`
			} `xml:"URLTemplate"`
		} `xml:"ChildObjects"`
	} `xml:"HTTPService"`
}

// grantedUseRights returns the set of object names that carry a Use=true right
// in Rights.xml. On 1С:Предприятие, Use is enforced per object with
// setForNewObjects=false, so an object that is absent from this set is NOT
// granted and a least-privilege user is denied.
func grantedUseRights(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := Source.ReadFile(rightsPath)
	if err != nil {
		t.Fatalf("read embedded %s: %v", rightsPath, err)
	}
	var doc rightsDoc
	if err := xml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", rightsPath, err)
	}
	granted := make(map[string]bool)
	for _, obj := range doc.Objects {
		for _, r := range obj.Rights {
			if r.Name == "Use" && r.Value == "true" {
				granted[obj.Name] = true
			}
		}
	}
	if len(granted) == 0 {
		t.Fatalf("parsed no Use grants from %s (parser or file broken)", rightsPath)
	}
	return granted
}

// TestRightsCoverAllServiceEndpoints asserts that MCP_ОсновнаяРоль grants Use
// for the HTTP service itself and for every URLTemplate and Method declared in
// MCPService.xml. It fails if any endpoint (e.g. a newly added /subsystems) is
// missing a grant, which would 403 a least-privilege user.
func TestRightsCoverAllServiceEndpoints(t *testing.T) {
	granted := grantedUseRights(t)

	raw, err := Source.ReadFile(mcpServicePath)
	if err != nil {
		t.Fatalf("read embedded %s: %v", mcpServicePath, err)
	}
	var svc mcpServiceDoc
	if err := xml.Unmarshal(raw, &svc); err != nil {
		t.Fatalf("parse %s: %v", mcpServicePath, err)
	}

	serviceName := svc.HTTPService.Properties.Name
	if serviceName == "" {
		t.Fatalf("could not read HTTPService name from %s (parser or file broken)", mcpServicePath)
	}
	templates := svc.HTTPService.ChildObjects.URLTemplates
	if len(templates) == 0 {
		t.Fatalf("parsed no URLTemplates from %s (parser or file broken)", mcpServicePath)
	}

	// Service-level Use grant.
	serviceKey := "HTTPService." + serviceName
	if !granted[serviceKey] {
		t.Errorf("missing Use grant for HTTP service: %s", serviceKey)
	}

	// Per-URLTemplate and per-Method Use grants.
	for _, ut := range templates {
		if ut.Properties.Name == "" {
			t.Errorf("URLTemplate with empty name in %s", mcpServicePath)
			continue
		}
		templateKey := serviceKey + ".URLTemplate." + ut.Properties.Name
		if !granted[templateKey] {
			t.Errorf("missing Use grant for URLTemplate %q: %s", ut.Properties.Name, templateKey)
		}
		for _, m := range ut.ChildObjects.Methods {
			if m.Properties.Name == "" {
				t.Errorf("Method with empty name under URLTemplate %q in %s", ut.Properties.Name, mcpServicePath)
				continue
			}
			methodKey := templateKey + ".Method." + m.Properties.Name
			if !granted[methodKey] {
				t.Errorf("missing Use grant for method %q of URLTemplate %q: %s", m.Properties.Name, ut.Properties.Name, methodKey)
			}
		}
	}
}
