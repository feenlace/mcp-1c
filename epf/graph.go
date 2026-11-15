package epf

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// EnableGraph selects only the maintained two-object native graph contract.
func (b *Bridge) EnableGraph() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.session != "" || b.documentReadCutoff == nil || !b.documentDateWrite {
		return errors.New("EPF graph configuration refused")
	}
	b.graph = true
	return nil
}

const graphBusinessLimit = 48 << 10
const graphEnvelopeLimit = 6*(192<<10) + 1024

// GraphReceiptBusiness validates an original finite Graph receipt reference.
// Schema descriptors are opaque here; canonical native GR performs qualification.
func GraphReceiptBusiness(raw []byte) bool {
	if unfGraphBusiness(raw) {
		return true
	}
	var b struct {
		Contract       string          `json:"business_contract"`
		Pair           string          `json:"pair_binding"`
		SourceIdentity string          `json:"source_identity"`
		TargetIdentity string          `json:"target_identity"`
		Action         string          `json:"action"`
		Profile        string          `json:"profile"`
		Key            string          `json:"idempotency_key"`
		TargetBase     string          `json:"target_base_token"`
		Source         json.RawMessage `json:"source"`
		Document       string          `json:"target_document_uuid"`
		Organization   string          `json:"target_organization_uuid"`
	}
	if len(raw) > graphBusinessLimit || !closedReadObject(raw, &b, "business_contract", "pair_binding", "source_identity", "target_identity", "action", "profile", "idempotency_key", "target_base_token", "source", "target_document_uuid", "target_organization_uuid") || b.Contract != "graph_business/1" || b.Action != "graph_import" || b.Profile != "GRAPH-COUNTDOCS-1" || !graphHex(b.Pair) || !graphHex(b.TargetBase) || !strings.HasPrefix(b.Key, "graph:") || !graphUUID(strings.TrimPrefix(b.Key, "graph:")) || !graphUUID(b.Document) || !graphUUID(b.Organization) || b.SourceIdentity == "" || len(b.SourceIdentity) > 1024 || b.TargetIdentity == "" || len(b.TargetIdentity) > 1024 {
		return false
	}
	var source struct {
		Profile      string                     `json:"profile"`
		Base         string                     `json:"base_token"`
		Schema       map[string]json.RawMessage `json:"schema"`
		Document     json.RawMessage            `json:"document"`
		Organization json.RawMessage            `json:"organization"`
	}
	if !closedReadObject(b.Source, &source, "profile", "base_token", "schema", "document", "organization") || source.Profile != b.Profile || !graphHex(source.Base) || source.Base == b.TargetBase || len(source.Schema) == 0 {
		return false
	}
	var d struct {
		UUID         string `json:"uuid"`
		Version      string `json:"version"`
		Number       string `json:"number"`
		Date         string `json:"date"`
		Allowed      *bool  `json:"allowed"`
		Organization string `json:"organization_uuid"`
	}
	var o struct {
		UUID        string  `json:"uuid"`
		Version     string  `json:"version"`
		Code        string  `json:"code"`
		Description *string `json:"description"`
		Allowed     *bool   `json:"allowed"`
	}
	if !closedReadObject(source.Document, &d, "uuid", "version", "number", "date", "allowed", "organization_uuid") || !closedReadObject(source.Organization, &o, "uuid", "version", "code", "description", "allowed") {
		return false
	}
	_, err := time.Parse("2006-01-02T15:04:05", d.Date)
	return err == nil && graphUUID(d.UUID) && graphUUID(o.UUID) && d.Organization == o.UUID && d.UUID != b.Document && o.UUID != b.Organization && d.Version != "" && len(d.Version) <= 4096 && o.Version != "" && len(o.Version) <= 4096 && d.Number != "" && utf8.RuneCountInString(d.Number) <= 12 && o.Code != "" && utf8.RuneCountInString(o.Code) <= 9 && o.Description != nil && utf8.RuneCountInString(*o.Description) <= 100 && d.Allowed != nil && o.Allowed != nil
}
func graphUUID(s string) bool {
	return documentUUID.MatchString(s) && s != "00000000-0000-0000-0000-000000000000"
}
func graphHex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
func graphBody(raw []byte, commit, receipt bool) bool {
	var in struct {
		Contract  int    `json:"contract_version"`
		Operation string `json:"operation"`
		Business  string `json:"business"`
		Identity  string `json:"identity"`
		Deadline  string `json:"deadline"`
	}
	allowed := []string{"contract_version", "operation", "business"}
	if commit {
		allowed = append(allowed, "identity", "deadline")
	}
	if len(raw) > graphEnvelopeLimit || !closedReadObject(raw, &in, allowed...) || in.Contract != 1 {
		return false
	}
	if commit {
		return in.Operation == "commit" && in.Identity != "" && in.Deadline != "" && GraphReceiptBusiness([]byte(in.Business))
	}
	if receipt {
		return in.Operation == "receipt" && GraphReceiptBusiness([]byte(in.Business))
	}
	if in.Operation == "graph_prepare" || in.Operation == "graph_receipt" {
		return GraphReceiptBusiness([]byte(in.Business))
	}
	var b struct {
		Action  string          `json:"action"`
		Profile string          `json:"profile"`
		UUID    string          `json:"uuid"`
		Source  json.RawMessage `json:"source"`
	}
	allowed = []string{"action", "profile"}
	if in.Operation == "graph_export" {
		allowed = append(allowed, "uuid")
	} else if in.Operation != "graph_capability" && in.Operation != "graph_schema" {
		return false
	}
	var hint struct {
		Profile string `json:"profile"`
	}
	_ = json.Unmarshal([]byte(in.Business), &hint)
	if in.Operation == "graph_schema" && hint.Profile == "UNF-COUNTERPARTY-1" {
		allowed = append(allowed, "source")
	}
	return len(in.Business) <= 192<<10 && closedReadObject([]byte(in.Business), &b, allowed...) && b.Action == in.Operation && (b.Profile == "GRAPH-COUNTDOCS-1" || b.Profile == "UNF-COUNTERPARTY-1") && (b.Profile != "UNF-COUNTERPARTY-1" || in.Operation != "graph_schema" || unfGraphSource(b.Source)) && (in.Operation != "graph_export" || graphUUID(b.UUID))
}

// UNF descriptors and typed image fields are verified by the fixed stock native
// contract; transport admits only its existing closed envelope, never a method/code.
func unfGraphSource(raw []byte) bool {
	var s struct {
		Profile      string                     `json:"profile"`
		Base         string                     `json:"base_token"`
		Schema       map[string]json.RawMessage `json:"schema"`
		Counterparty json.RawMessage            `json:"counterparty"`
		References   []json.RawMessage          `json:"references"`
	}
	if !closedReadObjectNull(raw, &s, true, "profile", "base_token", "schema", "counterparty", "references") || s.Profile != "UNF-COUNTERPARTY-1" || !graphHex(s.Base) || len(s.Schema) != 5 || s.References == nil {
		return false
	}
	var image struct {
		UUID        string                     `json:"uuid"`
		Version     string                     `json:"version"`
		Code        string                     `json:"code"`
		Description *string                    `json:"description"`
		Fields      map[string]json.RawMessage `json:"fields"`
	}
	return closedReadObject(s.Counterparty, &image, "uuid", "version", "code", "description", "fields") && graphUUID(image.UUID) && image.Version != "" && len(image.Version) <= 4096 && image.Description != nil && len(image.Fields) == 51
}
func unfGraphBusiness(raw []byte) bool {
	var b struct {
		Contract       string          `json:"business_contract"`
		Pair           string          `json:"pair_binding"`
		SourceIdentity string          `json:"source_identity"`
		TargetIdentity string          `json:"target_identity"`
		Action         string          `json:"action"`
		Profile        string          `json:"profile"`
		Key            string          `json:"idempotency_key"`
		Source         json.RawMessage `json:"source"`
		Target         json.RawMessage `json:"target"`
		Root           string          `json:"root_uuid"`
		Derived        json.RawMessage `json:"derived"`
		RootDerived    []string        `json:"root_derived_fields"`
	}
	if len(raw) > 192<<10 || !closedReadObjectNull(raw, &b, true, "business_contract", "pair_binding", "source_identity", "target_identity", "action", "profile", "idempotency_key", "source", "target", "root_uuid", "derived", "root_derived_fields") || b.Contract != "unf_graph_business/1" || b.Profile != "UNF-COUNTERPARTY-1" || b.Action != "graph_import" || !graphHex(b.Pair) || !strings.HasPrefix(b.Key, "graph:") || !graphUUID(strings.TrimPrefix(b.Key, "graph:")) || !graphUUID(b.Root) || b.SourceIdentity == "" || len(b.SourceIdentity) > 1024 || b.TargetIdentity == "" || len(b.TargetIdentity) > 1024 || !unfGraphSource(b.Source) || len(b.RootDerived) != 7 {
		return false
	}
	var target struct {
		Profile    string                     `json:"profile"`
		Base       string                     `json:"base_token"`
		Schema     string                     `json:"schema_contract"`
		References []json.RawMessage          `json:"references"`
		Defaults   map[string]json.RawMessage `json:"defaults"`
		Context    map[string]json.RawMessage `json:"context"`
	}
	var derived struct {
		Kind     string                     `json:"kind"`
		Count    int                        `json:"count"`
		Owner    string                     `json:"owner_uuid"`
		Defaults map[string]json.RawMessage `json:"defaults"`
		Effects  []string                   `json:"effects"`
	}
	var source struct {
		Base         string `json:"base_token"`
		Counterparty struct {
			UUID string `json:"uuid"`
		} `json:"counterparty"`
	}
	_ = json.Unmarshal(b.Source, &source)
	return closedReadObjectNull(b.Target, &target, true, "profile", "base_token", "schema_contract", "references", "defaults", "context") && target.Profile == b.Profile && target.Schema == "unf_common_schema/1" && graphHex(target.Base) && target.Base != source.Base && b.Root != source.Counterparty.UUID && target.References != nil && len(target.Defaults) == 7 && len(target.Context) == 9 && closedReadObjectNull(b.Derived, &derived, true, "kind", "count", "owner_uuid", "defaults", "effects") && derived.Kind == "stock_buyer_contract" && derived.Count == 1 && derived.Owner == b.Root && len(derived.Defaults) == 7 && len(derived.Effects) == 2 && derived.Effects[0] == "ОсновныеДоговорыКонтрагента" && derived.Effects[1] == "СостоянияКонтрагентовБЭД"
}
