package onec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// CountPeriod is a half-open civil-time interval in the selected base, without a timezone.
type CountPeriod struct {
	Field string `json:"field"`
	Start string `json:"start"`
	End   string `json:"end"`
}
type CountReference struct {
	CatalogName string `json:"catalog_name"`
	UUID        string `json:"uuid"`
}
type CountOrganization struct {
	Field     string         `json:"field"`
	Reference CountReference `json:"reference"`
}
type RecordCountRequest struct {
	ObjectType   string             `json:"object_type"`
	ObjectName   string             `json:"object_name"`
	Period       *CountPeriod       `json:"period,omitempty"`
	Organization *CountOrganization `json:"organization,omitempty"`
}
type RecordCountResult struct {
	ObjectType   string             `json:"object_type"`
	ObjectName   string             `json:"object_name"`
	Count        int64              `json:"count"`
	Period       *CountPeriod       `json:"period,omitempty"`
	Organization *CountOrganization `json:"organization,omitempty"`
}

var countUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func countIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r != '_' && !unicode.IsLetter(r) && (i == 0 || !unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}
func validateCountRequest(r RecordCountRequest) error {
	if r.ObjectType != "Catalog" && r.ObjectType != "Document" {
		return fmt.Errorf("count supports Catalog and Document only")
	}
	if !countIdentifier(r.ObjectName) {
		return fmt.Errorf("object_name must be a metadata identifier")
	}
	if p := r.Period; p != nil {
		if !countIdentifier(p.Field) {
			return fmt.Errorf("period field must be a metadata identifier")
		}
		for _, v := range []string{p.Start, p.End} {
			t, e := time.Parse("2006-01-02T15:04:05", v)
			if e != nil || len(v) != 19 || t.Year() < 1 || t.Format("2006-01-02T15:04:05") != v {
				return fmt.Errorf("period requires exact valid civil dates YYYY-MM-DDTHH:MM:SS")
			}
		}
		if p.Start >= p.End {
			return fmt.Errorf("period start must precede end")
		}
	}
	if o := r.Organization; o != nil {
		if !countIdentifier(o.Field) || !countIdentifier(o.Reference.CatalogName) || !countUUID.MatchString(o.Reference.UUID) || strings.EqualFold(o.Reference.UUID, "00000000-0000-0000-0000-000000000000") {
			return fmt.Errorf("organization requires an admitted Catalog name and nonempty UUID")
		}
	}
	return nil
}

// countJSONFields enforces wire spelling before Go's case-insensitive struct decoder.
func countJSONFields(data []byte, allowed ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, fmt.Errorf("count input must be a JSON object")
	}
	for key := range fields {
		accepted := false
		for _, name := range allowed {
			if key == name {
				accepted = true
				break
			}
		}
		if !accepted {
			return nil, fmt.Errorf("unsupported count property: %q", key)
		}
	}
	return fields, nil
}

// UnmarshalJSON refuses unsupported spelling and null filters rather than silently dropping them.
func (r *RecordCountRequest) UnmarshalJSON(data []byte) error {
	fields, err := countJSONFields(data, "object_type", "object_name", "period", "organization")
	if err != nil {
		return err
	}
	if raw, ok := fields["period"]; ok {
		if _, err := countJSONFields(raw, "field", "start", "end"); err != nil {
			return err
		}
	}
	if raw, ok := fields["organization"]; ok {
		organization, err := countJSONFields(raw, "field", "reference")
		if err != nil {
			return err
		}
		if raw, ok := organization["reference"]; ok {
			if _, err := countJSONFields(raw, "catalog_name", "uuid"); err != nil {
				return err
			}
		}
	}
	type plain RecordCountRequest
	var v plain
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("one count request object is required")
	}
	*r = RecordCountRequest(v)
	return nil
}

// CountRecords counts permitted rows using the fixed native endpoint and this client's transport.
func (c *Client) CountRecords(ctx context.Context, r RecordCountRequest) (*RecordCountResult, error) {
	if err := validateCountRequest(r); err != nil {
		return nil, err
	}
	if c == nil {
		return nil, fmt.Errorf("live connection is required")
	}
	var wire struct {
		ObjectType   string             `json:"object_type"`
		ObjectName   string             `json:"object_name"`
		Count        *int64             `json:"count"`
		Period       *CountPeriod       `json:"period"`
		Organization *CountOrganization `json:"organization"`
	}
	if err := c.Post(ctx, "/count", r, &wire); err != nil {
		return nil, err
	}
	if wire.Count == nil || *wire.Count < 0 || wire.ObjectType != r.ObjectType || !strings.EqualFold(wire.ObjectName, r.ObjectName) || !reflect.DeepEqual(wire.Period, r.Period) || !reflect.DeepEqual(wire.Organization, r.Organization) {
		return nil, fmt.Errorf("invalid count response: expected exact selected identity, applied filters and nonnegative int64 count")
	}
	return &RecordCountResult{wire.ObjectType, wire.ObjectName, *wire.Count, wire.Period, wire.Organization}, nil
}
