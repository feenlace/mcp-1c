package epf

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

const documentWriteBusinessLimit = 256 << 10

// JSON string encoding can expand each byte sixfold; only validated ordinary writes use this envelope.
const documentWriteEnvelopeLimit = 6*documentWriteBusinessLimit + 8192

var documentIdentifier = regexp.MustCompile(`^[\pL_][\pL\pN_]*$`)
var documentUUID = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)
var documentDecimal = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func documentQualified(value, prefix string) bool {
	return strings.HasPrefix(value, prefix+".") && documentIdentifier.MatchString(strings.TrimPrefix(value, prefix+"."))
}
func documentWriteBusiness(raw []byte) bool {
	var tag struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal(raw, &tag)
	if strings.HasPrefix(tag.Action, "set_") {
		return documentSpecialBusiness(raw)
	}
	var in struct {
		Action  string                                  `json:"action"`
		Type    string                                  `json:"document_type"`
		UUID    string                                  `json:"uuid"`
		Version string                                  `json:"version"`
		Key     string                                  `json:"idempotency_key"`
		Fields  map[string]json.RawMessage              `json:"fields"`
		Tables  map[string][]map[string]json.RawMessage `json:"tables"`
	}
	if len(raw) > documentWriteBusinessLimit || !closedReadObject(raw, &in, "action", "document_type", "idempotency_key", "fields", "tables", "uuid", "version") || !documentQualified(in.Type, "Документ") || len(in.Type) > 200 || len(in.Key) < 1 || len(in.Key) > 128 || strings.TrimSpace(in.Key) != in.Key {
		return false
	}
	for _, c := range in.Key {
		if c < 32 || c > 126 {
			return false
		}
	}
	var props map[string]json.RawMessage
	_ = json.Unmarshal(raw, &props)
	switch in.Action {
	case "create":
		if props["uuid"] != nil || props["version"] != nil {
			return false
		}
	case "update":
		if !documentUUID.MatchString(in.UUID) || in.UUID == "00000000-0000-0000-0000-000000000000" || len(in.Version) == 0 || len(in.Version) > 512 {
			return false
		}
	default:
		return false
	}
	if len(in.Fields) > 128 || len(in.Tables) > 32 || !documentWriteFields(in.Fields) {
		return false
	}
	rows := 0
	for name, table := range in.Tables {
		if !documentIdentifier.MatchString(name) || table == nil {
			return false
		}
		rows += len(table)
		if rows > 1000 {
			return false
		}
		for _, row := range table {
			if row == nil || len(row) > 128 || !documentWriteFields(row) {
				return false
			}
		}
	}
	return true
}
func documentWriteFields(fields map[string]json.RawMessage) bool {
	for name, raw := range fields {
		var v struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		}
		if !documentIdentifier.MatchString(name) || !closedReadObject(raw, &v, "type", "value") || len(v.Value) == 0 {
			return false
		}
		switch v.Type {
		case "string", "date", "number":
			var value string
			if json.Unmarshal(v.Value, &value) != nil {
				return false
			}
			if v.Type == "date" {
				if t, e := time.Parse("2006-01-02T15:04:05", value); e != nil || t.Format("2006-01-02T15:04:05") != value {
					return false
				}
			}
			if v.Type == "number" {
				unsigned := strings.TrimPrefix(value, "-")
				precision := len(strings.ReplaceAll(unsigned, ".", ""))
				if strings.HasPrefix(unsigned, "0.") {
					precision--
				}
				if !documentDecimal.MatchString(value) || precision > 38 {
					return false
				}
			}
		case "bool":
			var value bool
			if json.Unmarshal(v.Value, &value) != nil {
				return false
			}
		case "reference":
			var ref struct {
				Type string `json:"type"`
				UUID string `json:"uuid"`
			}
			if !closedReadObject(v.Value, &ref, "type", "uuid") || !documentUUID.MatchString(strings.ToLower(ref.UUID)) || (!documentQualified(ref.Type, "Справочник") && !documentQualified(ref.Type, "Документ")) {
				return false
			}
		case "enum":
			var ref struct {
				Type   string `json:"type"`
				Member string `json:"member"`
			}
			if !closedReadObject(v.Value, &ref, "type", "member") || !documentQualified(ref.Type, "Перечисление") || !documentIdentifier.MatchString(ref.Member) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Special intents retain their own exact shapes; they never become ordinary fields.
func documentSpecialBusiness(raw []byte) bool {
	var in struct {
		Action        string          `json:"action"`
		Type          string          `json:"document_type"`
		UUID          string          `json:"uuid"`
		Version       string          `json:"version"`
		Key           string          `json:"idempotency_key"`
		Number        json.RawMessage `json:"number"`
		Context       json.RawMessage `json:"number_context"`
		Deleted       *bool           `json:"deleted"`
		Profile       string          `json:"amount_profile"`
		Amount        json.RawMessage `json:"amount"`
		AmountContext string          `json:"amount_context"`
	}
	var tag struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal(raw, &tag)
	allowed := []string{"action", "document_type", "uuid", "version", "idempotency_key"}
	switch tag.Action {
	case "set_number":
		allowed = append(allowed, "number", "number_context")
	case "set_mark":
		allowed = append(allowed, "deleted")
	case "set_amount":
		allowed = append(allowed, "amount_profile", "amount", "amount_context")
	default:
		return false
	}
	var props map[string]json.RawMessage
	if len(raw) > documentWriteBusinessLimit || !closedReadObject(raw, &in, allowed...) || json.Unmarshal(raw, &props) != nil || len(props) != len(allowed) {
		return false
	}
	target, _ := json.Marshal(map[string]any{"action": "update", "document_type": in.Type, "uuid": in.UUID, "version": in.Version, "idempotency_key": in.Key})
	if !documentWriteBusiness(target) {
		return false
	}
	switch in.Action {
	case "set_mark":
		return in.Deleted != nil
	case "set_number":
		var n struct {
			Contract  string `json:"contract"`
			Type      string `json:"type"`
			Length    int    `json:"length"`
			Allowed   string `json:"allowed_length"`
			Auto      bool   `json:"autonumbering"`
			Unique    bool   `json:"check_unique"`
			Period    string `json:"periodicity"`
			Numerator string `json:"numerator"`
		}
		var v struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		}
		var fields map[string]json.RawMessage
		return closedReadObject(in.Context, &n, "contract", "type", "length", "allowed_length", "autonumbering", "check_unique", "periodicity", "numerator") && json.Unmarshal(in.Context, &fields) == nil && len(fields) == 8 && n.Contract == "draft_number_metadata/1" && n.Type == "string" && n.Length > 0 && n.Allowed == "variable" && n.Unique && n.Period == "nonperiodical" && n.Numerator == "none" && closedReadObject(in.Number, &v, "type", "value") && v.Type == "string" && strings.TrimSpace(v.Value) != "" && len(utf16.Encode([]rune(v.Value))) <= n.Length
	case "set_amount":
		return in.Type == "Документ.ПлатежноеПоручение" && in.Profile == "UNF-PAYMENT-AMOUNT-1" && len(in.AmountContext) == 64 && strings.Trim(in.AmountContext, "0123456789abcdef") == "" && documentAmountValue(in.Amount)
	}
	return false
}
func documentAmountValue(raw []byte) bool {
	var v struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	if !closedReadObject(raw, &v, "type", "value") || v.Type != "number" || !documentDecimal.MatchString(v.Value) || strings.HasPrefix(v.Value, "-") {
		return false
	}
	parts := strings.Split(v.Value, ".")
	return len(parts[0]) <= 13 && (len(parts) == 1 || len(parts[1]) <= 2 && !strings.HasSuffix(parts[1], "0"))
}
