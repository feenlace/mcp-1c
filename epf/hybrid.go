package epf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// EnableDocumentRead is a finite opt-in for the Pro server, before serving/pairing.
// The callback must return the verified grant cutoff, never a worker-supplied date.
func (b *Bridge) EnableDocumentRead(cutoff func(context.Context) (time.Time, error)) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.session != "" || cutoff == nil || b.documentReadCutoff != nil {
		return errors.New("EPF document READ configuration refused")
	}
	b.documentReadCutoff = cutoff
	return nil
}

// EnableDocumentDateWrite enables finite document mutations and owner receipts.
// The historical capability name is retained for existing Date workers.
// Receipt observation is not a new paid execution admission.
func (b *Bridge) EnableDocumentDateWrite() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.session != "" || b.documentReadCutoff == nil {
		return errors.New("EPF document Date configuration refused")
	}
	b.documentDateWrite = true
	return nil
}

func documentDateBody(raw []byte, commit, receipt bool) bool {
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
	if !closedReadObject(raw, &in, allowed...) || in.Contract != 1 {
		return false
	}
	if receipt && in.Operation != "receipt" || commit && (in.Operation != "commit" || in.Identity == "" || in.Deadline == "") || !receipt && !commit && in.Operation != "prepare" && in.Operation != "identity" {
		return false
	}
	return documentWriteBusiness([]byte(in.Business))
}
func (b *Bridge) readAdmitted(ctx context.Context, task *Task) bool {
	if b.documentReadCutoff == nil {
		return false
	}
	cutoff, err := b.documentReadCutoff(ctx)
	// A replacement grant may shorten, but never extend, the admitted task.
	if err != nil || !time.Now().Before(cutoff) {
		return false
	}
	if cutoff.Before(task.ExecuteBefore) {
		task.ExecuteBefore = cutoff
	}
	return true
}

func closedReadObject(raw []byte, out any, allowed ...string) bool {
	return closedReadObjectNull(raw, out, false, allowed...)
}
func closedReadObjectNull(raw []byte, out any, allowNull bool, allowed ...string) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth > 16 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		if token == nil {
			return allowNull && depth != 0
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					key, err := d.Token()
					name, ok := key.(string)
					if err != nil || !ok || seen[name] {
						return false
					}
					seen[name] = true
					if depth == 0 && !slices.Contains(allowed, name) {
						return false
					}
					if !walk(depth + 1) {
						return false
					}
				}
			case '[':
				for d.More() {
					if !walk(depth + 1) {
						return false
					}
				}
			default:
				return false
			}
			_, err = d.Token()
			return err == nil
		}
		return depth != 0
	}
	if !walk(0) {
		return false
	}
	if _, err := d.Token(); err != io.EOF {
		return false
	}
	return json.Unmarshal(raw, out) == nil
}
func documentReadBody(raw []byte) bool {
	var in struct {
		Contract  int    `json:"contract_version"`
		Operation string `json:"operation"`
		Business  string `json:"business"`
	}
	if !closedReadObject(raw, &in, "contract_version", "operation", "business") || in.Contract != 1 || !slices.Contains([]string{"identity", "describe", "get", "get_date", "get_number", "get_mark", "get_amount", "get_amount_state"}, in.Operation) {
		return false
	}
	var business struct {
		Action  string          `json:"action"`
		Type    string          `json:"document_type"`
		UUID    string          `json:"uuid"`
		Deleted *bool           `json:"deleted"`
		Profile string          `json:"amount_profile"`
		Amount  json.RawMessage `json:"amount"`
	}
	allowed := []string{"action", "document_type", "uuid"}
	switch in.Operation {
	case "get_date", "get_number":
	case "get_mark":
		allowed = append(allowed, "deleted")
	case "get_amount":
		allowed = append(allowed, "amount_profile", "amount")
	case "get_amount_state":
	default:
		if !closedReadObject([]byte(in.Business), &business, "action", "document_type", "uuid", "field", "reference_type", "search", "limit") {
			return false
		}
		return (business.Action == "describe" || business.Action == "get") && (in.Operation == "identity" || in.Operation == business.Action)
	}
	var props map[string]json.RawMessage
	if !closedReadObject([]byte(in.Business), &business, allowed...) || json.Unmarshal([]byte(in.Business), &props) != nil || len(props) != len(allowed) || !documentQualified(business.Type, "Документ") || len(business.Type) > 200 || !documentUUID.MatchString(business.UUID) || business.UUID == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	switch in.Operation {
	case "get_date", "get_number":
		return business.Action == "get"
	case "get_mark":
		return business.Action == "get_mark" && business.Deleted != nil
	case "get_amount":
		return business.Action == "get_amount" && business.Type == "Документ.ПлатежноеПоручение" && business.Profile == "UNF-PAYMENT-AMOUNT-1" && documentAmountValue(business.Amount)
	case "get_amount_state":
		return business.Action == "get_amount_state" && business.Type == "Документ.ПлатежноеПоручение"
	}
	return false
}

// PinSession binds successive requests to one currently paired worker/core.
// It is continuity proof, not admission or permission to replay a request.
func (b *Bridge) PinSession(ctx context.Context) (context.Context, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.session == "" {
		return nil, ErrUnavailable
	}
	if pin, ok := ctx.Value(sessionPinKey{}).(sessionPin); ok && (pin.bridge != b || pin.session != b.session || pin.identity != b.identity || pin.core != b.core) {
		return nil, ErrSessionChanged
	}
	return context.WithValue(ctx, sessionPinKey{}, sessionPin{b, b.session, b.identity, b.core}), nil
}

// PinDocument freezes admission for a multi-call document workflow.
// Late results may be collected; a replacement grant cannot admit another call.
func (b *Bridge) PinDocument(ctx context.Context) (context.Context, error) {
	pinned, err := b.PinSession(ctx)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	check, enabled := b.documentReadCutoff, b.documentDateWrite
	b.mu.Unlock()
	if check == nil || !enabled {
		return nil, ErrCapabilityUnavailable
	}
	cutoff, err := check(ctx)
	if err != nil {
		return nil, err
	}
	if !time.Now().Before(cutoff) {
		return nil, context.DeadlineExceeded
	}
	if prior, ok := ctx.Value(documentCutoffKey{}).(time.Time); ok && prior.Before(cutoff) {
		cutoff = prior
	}
	return context.WithValue(pinned, documentCutoffKey{}, cutoff), nil
}

// PinDocumentUntil adds a caller's original verified execution ceiling without
// cancelling collection. It can only shorten the current authenticated grant.
func (b *Bridge) PinDocumentUntil(ctx context.Context, ceiling time.Time) (context.Context, error) {
	if ceiling.IsZero() || !time.Now().Before(ceiling) {
		return nil, context.DeadlineExceeded
	}
	pinned, err := b.PinDocument(ctx)
	if err != nil {
		return nil, err
	}
	if current, ok := pinned.Value(documentCutoffKey{}).(time.Time); ok && current.Before(ceiling) {
		ceiling = current
	}
	return context.WithValue(pinned, documentCutoffKey{}, ceiling), nil
}

type documentCutoffKey struct{}

type sessionPinKey struct{}
type sessionPin struct {
	bridge                  *Bridge
	session, identity, core string
}

// EnableQueryPackage opts a paid server into the fixed native package contract.
func (b *Bridge) EnableQueryPackage(cutoff func(context.Context) (time.Time, error)) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.session != "" || cutoff == nil || b.queryPackageCutoff != nil {
		return errors.New("EPF query package configuration refused")
	}
	b.queryPackageCutoff = cutoff
	return nil
}
func (b *Bridge) packageAdmitted(ctx context.Context, task *Task) bool {
	cutoff, err := b.queryPackageCutoff(ctx)
	if err != nil || !time.Now().Before(cutoff) {
		return false
	}
	if cutoff.Before(task.ExecuteBefore) {
		task.ExecuteBefore = cutoff
	}
	return true
}
func queryPackageBody(raw []byte) bool {
	var in struct {
		Query string `json:"query"`
		Run   string `json:"run_id"`
	}
	return closedReadObject(raw, &in, "query", "parameters", "result_indices", "row_limit", "timeout", "run_id") && in.Query != "" && in.Run != ""
}

// EnableObjectDiff adds only the existing fixed Advanced comparison after its live grant callback is configured.
func (b *Bridge) EnableObjectDiff() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.session != "" || b.queryPackageCutoff == nil {
		return errors.New("EPF object diff configuration refused")
	}
	b.objectDiff = true
	return nil
}
func objectDiffBody(raw []byte) bool {
	var in struct {
		Contract string              `json:"contract_version"`
		Run      string              `json:"run_id"`
		Catalog  string              `json:"catalog_type"`
		Left     string              `json:"left_uuid"`
		Right    string              `json:"right_uuid"`
		Rows     map[string][]string `json:"row_keys"`
	}
	return closedReadObject(raw, &in, "contract_version", "run_id", "catalog_type", "left_uuid", "right_uuid", "row_keys") && in.Contract == "object_diff/1" && in.Run != "" && in.Catalog != "" && in.Left != "" && in.Right != "" && in.Rows != nil
}

// EnableJobDiagnose adds only the existing selected-job read under the live Advanced cutoff.
func (b *Bridge) EnableJobDiagnose() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.session != "" || b.queryPackageCutoff == nil {
		return errors.New("EPF job diagnosis configuration refused")
	}
	b.jobDiagnose = true
	return nil
}
func jobDiagnoseBody(raw []byte) bool {
	var in struct {
		Contract string `json:"contract_version"`
		Request  string `json:"request_id"`
		Kind     string `json:"job_kind"`
		Job      string `json:"job_id"`
		Start    string `json:"start_date"`
		End      string `json:"end_date"`
	}
	return closedReadObject(raw, &in, "contract_version", "request_id", "job_kind", "job_id", "start_date", "end_date") && in.Contract == "JOB-DIAG-1" && in.Request != "" && (in.Kind == "background" || in.Kind == "scheduled") && in.Job != "" && in.Start != "" && in.End != ""
}

// EnableIncidentTimeline opts Pro into the existing finite context read under its live grant.
func (b *Bridge) EnableIncidentTimeline() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.session != "" || b.documentReadCutoff == nil {
		return errors.New("EPF incident configuration refused")
	}
	b.incident = true
	return nil
}
func incidentBody(raw []byte) bool {
	var in struct {
		Contract string `json:"contract_version"`
		Request  string `json:"request_id"`
		Kind     string `json:"job_kind"`
		Job      string `json:"job_id"`
		Start    string `json:"start_date"`
		End      string `json:"end_date"`
		Session  *int64 `json:"session_id,omitempty"`
		Storage  *struct {
			Type string `json:"object_type"`
			Name string `json:"object_name"`
		} `json:"storage_object,omitempty"`
	}
	return closedReadObject(raw, &in, "contract_version", "request_id", "job_kind", "job_id", "start_date", "end_date", "session_id", "storage_object") && in.Contract == "INCIDENT-TIMELINE-1" && in.Request != "" && (in.Kind == "background" || in.Kind == "scheduled") && in.Job != "" && in.Start != "" && in.End != "" && (in.Session == nil || *in.Session >= 1 && *in.Session <= 9007199254740991) && (in.Storage == nil || in.Storage.Type != "" && in.Storage.Name != "")
}

// EnableExchangeQueue adds only the canonical queue describe/read under the live Pro cutoff.
func (b *Bridge) EnableExchangeQueue() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.session != "" || b.documentReadCutoff == nil {
		return errors.New("EPF exchange queue configuration refused")
	}
	b.exchangeQueue = true
	return nil
}
func exchangeQueueBody(raw []byte) bool {
	var in struct {
		Contract  int    `json:"contract_version"`
		Operation string `json:"operation"`
		Business  string `json:"business"`
		Identity  string `json:"identity"`
	}
	if !closedReadObject(raw, &in, "contract_version", "operation", "business", "identity") || in.Contract != 1 || (in.Operation != "queue_describe" && in.Operation != "queue_read") || (in.Operation == "queue_read") != (in.Identity != "") {
		return false
	}
	var business struct {
		Action  string `json:"action"`
		Profile string `json:"profile"`
		Node    string `json:"node_uuid"`
		Limit   int    `json:"limit"`
		Binding *struct {
			Principal string          `json:"principal_uuid"`
			Base      string          `json:"base_token"`
			Schema    json.RawMessage `json:"schema"`
			Node      string          `json:"node_uuid"`
			Local     *bool           `json:"is_local"`
		} `json:"binding"`
	}
	if !closedReadObject([]byte(in.Business), &business, "action", "profile", "node_uuid", "limit", "binding") || business.Action != in.Operation || (business.Profile != "R12-QUEUE-ORG-1" && business.Profile != "UNF-BP30-COUNTERPARTY-1") || business.Node == "" || business.Limit != 1 {
		return false
	}
	// Native admission validates the fixed schema, UUIDs and every binding field.
	return (in.Operation == "queue_describe" && business.Binding == nil) || (in.Operation == "queue_read" && business.Binding != nil && business.Binding.Principal != "" && business.Binding.Base != "" && len(business.Binding.Schema) != 0 && business.Binding.Node == business.Node && business.Binding.Local != nil)
}

// EnableReconcile adds only the fixed native identity/selection contract under Pro admission.
func (b *Bridge) EnableReconcile() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.session != "" || b.documentReadCutoff == nil {
		return errors.New("EPF reconcile configuration refused")
	}
	b.reconcile = true
	return nil
}
func reconcileBody(raw []byte) bool {
	var in struct {
		Contract  string  `json:"contract_version"`
		Run       string  `json:"run_id"`
		Operation string  `json:"operation"`
		Package   *string `json:"package_text,omitempty"`
	}
	if !closedReadObject(raw, &in, "contract_version", "run_id", "operation", "package_text") || in.Contract != "reconcile_read/1" || in.Run == "" {
		return false
	}
	if in.Operation == "identity" {
		return in.Package == nil && !bytes.Contains(raw, []byte(`"package_text"`))
	}
	return in.Operation == "selection" && in.Package != nil && len(*in.Package) <= 524288 && queryPackageBody([]byte(*in.Package))
}

// PinReconcile freezes the verified admission cutoff without cancelling late-result collection.
func (b *Bridge) PinReconcile(ctx context.Context) (context.Context, error) {
	pinned, err := b.PinSession(ctx)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	check := b.documentReadCutoff
	enabled := b.reconcile
	b.mu.Unlock()
	if check == nil || !enabled {
		return nil, ErrCapabilityUnavailable
	}
	cutoff, err := check(ctx)
	if err != nil {
		return nil, err
	}
	if !time.Now().Before(cutoff) {
		return nil, context.DeadlineExceeded
	}
	if prior, ok := ctx.Value(reconcileCutoffKey{}).(time.Time); ok && prior.Before(cutoff) {
		cutoff = prior
	}
	return context.WithValue(pinned, reconcileCutoffKey{}, cutoff), nil
}

type reconcileCutoffKey struct{}

// EnableQuality adds only the fixed selected-quality contract under verified Pro admission.
func (b *Bridge) EnableQuality() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.session != "" || b.documentReadCutoff == nil {
		return errors.New("EPF quality configuration refused")
	}
	b.quality = true
	return nil
}
func qualityBody(raw []byte) bool {
	var in struct {
		Contract   string          `json:"contract_version"`
		Run        string          `json:"run_id"`
		Operation  string          `json:"operation"`
		Definition json.RawMessage `json:"definition"`
		Timeout    int             `json:"timeout"`
	}
	if !closedReadObjectNull(raw, &in, true, "contract_version", "run_id", "operation", "definition", "timeout") || in.Contract != "quality_read/1" || in.Run == "" || in.Timeout < 1 || in.Timeout > 120 {
		return false
	}
	if in.Operation == "identity" {
		return bytes.Equal(bytes.TrimSpace(in.Definition), []byte("null"))
	}
	if in.Operation != "inspect" && in.Operation != "read" && in.Operation != "revalidate" {
		return false
	}
	var d struct {
		Catalog   string            `json:"catalog_type"`
		Rules     []string          `json:"rules"`
		Locations []json.RawMessage `json:"locations"`
	}
	if !closedReadObject(in.Definition, &d, "catalog_type", "rules", "locations") || d.Catalog == "" || len(d.Rules) < 1 || len(d.Rules) > 2 || d.Locations == nil || len(d.Locations) > 16 {
		return false
	}
	seen := map[string]bool{}
	for _, rule := range d.Rules {
		if (rule != "code_exact" && rule != "name_trim_lower_v1") || seen[rule] {
			return false
		}
		seen[rule] = true
	}
	for _, raw := range d.Locations {
		var l struct {
			Document string   `json:"document_type"`
			Table    string   `json:"table,omitempty"`
			Field    string   `json:"field"`
			Keys     []string `json:"row_keys"`
		}
		if !closedReadObject(raw, &l, "document_type", "table", "field", "row_keys") || l.Document == "" || l.Field == "" || l.Keys == nil || len(l.Keys) > 8 || (l.Table == "" && len(l.Keys) != 0) || (l.Table != "" && len(l.Keys) == 0) {
			return false
		}
	}
	return true
}

// PinQuality freezes the verified admission cutoff without cancelling late-result collection.
func (b *Bridge) PinQuality(ctx context.Context) (context.Context, error) {
	pinned, err := b.PinSession(ctx)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	check := b.documentReadCutoff
	enabled := b.quality
	b.mu.Unlock()
	if check == nil || !enabled {
		return nil, ErrCapabilityUnavailable
	}
	cutoff, err := check(ctx)
	if err != nil {
		return nil, err
	}
	if !time.Now().Before(cutoff) {
		return nil, context.DeadlineExceeded
	}
	if prior, ok := ctx.Value(qualityCutoffKey{}).(time.Time); ok && prior.Before(cutoff) {
		cutoff = prior
	}
	return context.WithValue(pinned, qualityCutoffKey{}, cutoff), nil
}

type qualityCutoffKey struct{}

// EnableReportCompose adds only the fixed selected-quality contract under verified Pro admission.
func (b *Bridge) EnableReportCompose() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.session != "" || b.documentReadCutoff == nil {
		return errors.New("EPF report composition configuration refused")
	}
	b.reportCompose = true
	return nil
}

// PinReportCompose freezes the verified admission cutoff without cancelling late-result collection.
func (b *Bridge) PinReportCompose(ctx context.Context) (context.Context, error) {
	pinned, err := b.PinSession(ctx)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	check := b.documentReadCutoff
	enabled := b.reportCompose
	b.mu.Unlock()
	if check == nil || !enabled {
		return nil, ErrCapabilityUnavailable
	}
	cutoff, err := check(ctx)
	if err != nil {
		return nil, err
	}
	if !time.Now().Before(cutoff) {
		return nil, context.DeadlineExceeded
	}
	if prior, ok := ctx.Value(reportComposeCutoffKey{}).(time.Time); ok && prior.Before(cutoff) {
		cutoff = prior
	}
	return context.WithValue(pinned, reportComposeCutoffKey{}, cutoff), nil
}

type reportComposeCutoffKey struct{}

func reportComposeBody(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	var in struct {
		Contract   string          `json:"contract_version"`
		Run        string          `json:"run_id"`
		Operation  string          `json:"operation"`
		Definition json.RawMessage `json:"definition"`
		Parameters json.RawMessage `json:"parameters"`
		Schema     *string         `json:"schema_xml,omitempty"`
		Identity   *string         `json:"expected_identity_sha256,omitempty"`
		Context    *string         `json:"expected_context_sha256,omitempty"`
	}
	if !closedReadObjectNull(raw, &in, true, "contract_version", "run_id", "operation", "definition", "parameters", "schema_xml", "expected_identity_sha256", "expected_context_sha256") || in.Run == "" || len(in.Definition) == 0 || len(in.Parameters) == 0 || in.Definition[0] != '{' || in.Parameters[0] != '{' {
		return false
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	for _, name := range []string{"schema_xml", "expected_identity_sha256", "expected_context_sha256"} {
		if bytes.Equal(fields[name], []byte("null")) {
			return false
		}
	}
	if in.Contract == "report_compose/1" {
		if in.Context != nil {
			return false
		}
		if in.Operation == "compile" {
			return in.Schema == nil && in.Identity == nil
		}
		return in.Operation == "compose" && in.Schema != nil && in.Identity != nil && len(*in.Identity) == 64
	}
	return in.Contract == "report_replay/1" && in.Schema != nil && in.Identity != nil && len(*in.Identity) == 64 && in.Context != nil && ((in.Operation == "capture" && *in.Context == "") || (in.Operation == "replay" && len(*in.Context) == 64))
}

// EnablePrintExport opts Pro into only the four existing bounded print/export operations.
func (b *Bridge) EnablePrintExport() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.session != "" || b.documentReadCutoff == nil {
		return errors.New("EPF print/export configuration refused")
	}
	b.printExport = true
	return nil
}
func printExportBody(raw []byte) bool {
	var in struct {
		Contract     int      `json:"contract_version"`
		Operation    string   `json:"operation"`
		DocumentType string   `json:"document_type"`
		UUID         string   `json:"uuid"`
		Command      string   `json:"command"`
		Format       string   `json:"format"`
		From         string   `json:"from"`
		To           string   `json:"to"`
		Columns      []string `json:"columns"`
		Filters      map[string]struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"filters"`
		Report     string `json:"report"`
		Variant    string `json:"variant"`
		Parameters map[string]struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"parameters"`
	}
	if !utf8.Valid(raw) || !closedReadObject(raw, &in, "contract_version", "operation", "document_type", "uuid", "command", "format", "from", "to", "columns", "filters", "report", "variant", "parameters") || in.Contract != 1 {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	allowed := "contract_version,operation,"
	switch in.Operation {
	case "describe":
		allowed += "document_type,uuid,report,"
	case "document":
		allowed += "document_type,uuid,command,format,"
		if in.DocumentType == "" || in.UUID == "" || in.Command == "" {
			return false
		}
	case "document_list":
		allowed += "document_type,from,to,columns,filters,format,"
		if in.DocumentType == "" || in.From == "" || in.To == "" || in.Format != "xlsx" {
			return false
		}
	case "report":
		allowed += "report,variant,parameters,format,"
		if in.Report == "" || in.Variant == "" {
			return false
		}
	default:
		return false
	}
	for key := range fields {
		if !slices.Contains(strings.Split(allowed, ","), key) {
			return false
		}
	}
	return in.Operation == "describe" || in.Format == "pdf" || in.Format == "xlsx"
}
func printExportFileBody(raw []byte) bool {
	if !printExportBody(raw) {
		return false
	}
	var in struct {
		Operation string `json:"operation"`
	}
	return json.Unmarshal(raw, &in) == nil && in.Operation != "describe"
}

// EnableCodeExecute enables only the existing Advanced arbitrary-code endpoint.
// The owning handler validates sandbox mode and explicit full-mode confirmation.
func (b *Bridge) EnableCodeExecute() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.session != "" || b.queryPackageCutoff == nil {
		return errors.New("EPF code execution configuration refused")
	}
	b.codeExecute = true
	return nil
}
func codeExecuteBody(raw []byte) bool {
	var in struct {
		Code    string `json:"code"`
		Mode    string `json:"mode"`
		Timeout int    `json:"timeout"`
	}
	return utf8.Valid(raw) && closedReadObject(raw, &in, "code", "mode", "timeout") && len(in.Code) > 0 && len(in.Code) <= 10240 && (in.Mode == "read_only" || in.Mode == "full") && in.Timeout >= 1 && in.Timeout <= 120
}

// documentOwnerIdentityBody admits no schema, business fields or mutation.
func documentOwnerIdentityBody(raw []byte) bool {
	var in struct {
		Contract  int    `json:"contract_version"`
		Operation string `json:"operation"`
		Business  string `json:"business"`
	}
	var business struct {
		Action string `json:"action"`
	}
	return closedReadObject(raw, &in, "contract_version", "operation", "business") && in.Contract == 1 && in.Operation == "identity" && closedReadObject([]byte(in.Business), &business, "action") && business.Action == "describe"
}

// DocumentOwnerBinding must follow a successful native document identity call on
// this pin: EPFДокументыЧтение revalidates the paired core before/after that call.
// Pair metadata alone is not authenticated native identity evidence. The digest
// binds logical native base/install lineage, not physical clone isolation.
func (b *Bridge) DocumentOwnerBinding(ctx context.Context, principal, configName, configVersion string) (string, error) {
	core, err := b.documentOwnerCore(ctx, principal, configName, configVersion)
	if err != nil {
		return "", err
	}
	raw, _ := json.Marshal([]string{"draft_owner/1", core.Principal, core.ConfigName, core.ConfigVersion, core.Owner, core.Install, core.Version, core.Base})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// DocumentOwnerBase follows the same successful native technical identity proof
// as DocumentOwnerBinding; pair metadata or a stored row is never authority.
func (b *Bridge) DocumentOwnerBase(ctx context.Context, principal, configName, configVersion string) (string, error) {
	core, err := b.documentOwnerCore(ctx, principal, configName, configVersion)
	if err != nil {
		return "", err
	}
	return core.Base, nil
}

type documentOwnerCore struct {
	Contract      string `json:"epf_contract"`
	Identity      int    `json:"identity_contract"`
	Principal     string `json:"principal_uuid"`
	ConfigName    string `json:"config_name"`
	ConfigVersion string `json:"config_version"`
	Owner         string `json:"core_owner"`
	Install       string `json:"core_install"`
	Version       string `json:"core_version"`
	Base          string `json:"base"`
	Session       string `json:"native_session"`
}

func (b *Bridge) documentOwnerCore(ctx context.Context, principal, configName, configVersion string) (documentOwnerCore, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	pin, ok := ctx.Value(sessionPinKey{}).(sessionPin)
	if !ok || b.closed || b.session == "" || pin.bridge != b || pin.session != b.session || pin.identity != b.identity || pin.core != b.core {
		return documentOwnerCore{}, ErrSessionChanged
	}
	var core documentOwnerCore
	if !closedReadObject([]byte(b.core), &core, "epf_contract", "identity_contract", "principal_uuid", "config_name", "config_version", "core_owner", "core_install", "core_version", "base", "native_session") || core.Contract != "document-read/1" || core.Identity != 1 || !graphUUID(core.Principal) || core.Principal != principal || core.ConfigName == "" || core.ConfigName != configName || core.ConfigVersion == "" || core.ConfigVersion != configVersion || core.Owner != "MCP_HTTPService" || !graphUUID(core.Install) || core.Version == "" || !graphHex(core.Base) || core.Session == "" {
		return documentOwnerCore{}, ErrHybridUnavailable
	}
	for _, c := range core.Session {
		if c < '0' || c > '9' {
			return documentOwnerCore{}, ErrHybridUnavailable
		}
	}
	return core, nil
}

// PinOwnerObservation enables only the exact technical identity request on the
// read-only receipt lane. It grants no business read, paid admission or replay.
func (b *Bridge) PinOwnerObservation(ctx context.Context) (context.Context, error) {
	pinned, err := b.PinSession(ctx)
	if err != nil {
		return nil, err
	}
	return context.WithValue(pinned, ownerObservationKey{}, true), nil
}

type ownerObservationKey struct{}

// PinCodeExecute freezes the original signed Advanced cutoff without cancelling late collection.
func (b *Bridge) PinCodeExecute(ctx context.Context) (context.Context, error) {
	pinned, err := b.PinSession(ctx)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	check := b.queryPackageCutoff
	enabled := b.codeExecute
	b.mu.Unlock()
	if check == nil || !enabled {
		return nil, ErrHybridUnavailable
	}
	cutoff, err := check(ctx)
	if err != nil {
		return nil, err
	}
	if prior, ok := ctx.Value(codeExecuteCutoffKey{}).(time.Time); ok && prior.Before(cutoff) {
		cutoff = prior
	}
	if !time.Now().Before(cutoff) {
		return nil, context.DeadlineExceeded
	}
	return context.WithValue(pinned, codeExecuteCutoffKey{}, cutoff), nil
}

type codeExecuteCutoffKey struct{}

// EnableJobRead opts Pro into selected stock-job observations only.
func (b *Bridge) EnableJobRead() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.session != "" || b.documentReadCutoff == nil {
		return errors.New("EPF job read configuration refused")
	}
	b.jobRead = true
	return nil
}

// PinJobRead preserves one session/core and the original Pro execution grant.
func (b *Bridge) PinJobRead(ctx context.Context) (context.Context, error) {
	pinned, err := b.PinSession(ctx)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	check, enabled := b.documentReadCutoff, b.jobRead
	b.mu.Unlock()
	if check == nil || !enabled {
		return nil, ErrCapabilityUnavailable
	}
	cutoff, err := check(ctx)
	if err != nil {
		return nil, err
	}
	if !time.Now().Before(cutoff) {
		return nil, context.DeadlineExceeded
	}
	if prior, ok := ctx.Value(jobReadCutoffKey{}).(time.Time); ok && prior.Before(cutoff) {
		cutoff = prior
	}
	return context.WithValue(pinned, jobReadCutoffKey{}, cutoff), nil
}

type jobReadCutoffKey struct{}

func jobReadCapability(cap string) bool {
	return cap == "pro.job-control-read" || cap == "pro.job-retry-read"
}

func jobReadBody(raw []byte, retry, identity bool) bool {
	fields := []string{"contract_version", "profile", "request_id", "job_id", "schedule_id", "deadline", "expected_identity", "expected_start"}
	if retry {
		fields = append(fields, "expected_composition", "expected_version_id", "expected_update_id", "key", "new_run_id", "new_run_start", "expected_base_token")
	}
	var in map[string]json.RawMessage
	limit := 4096
	if retry {
		limit = 32768
	}
	if len(raw) > limit || !utf8.Valid(raw) || !closedReadObject(raw, &in, fields...) || len(in) != len(fields) {
		return false
	}
	var contract int
	if json.Unmarshal(in["contract_version"], &contract) != nil || (!retry && contract != 1) || (retry && contract != 2) {
		return false
	}
	values := map[string]string{}
	for _, key := range fields[1:] {
		var value string
		if json.Unmarshal(in[key], &value) != nil {
			return false
		}
		values[key] = value
	}
	if (!retry && values["profile"] != "JOB-CANCEL-FTS-1") || (retry && values["profile"] != "JOB-RETRY-FTS-1") || !graphUUID(values["request_id"]) {
		return false
	}
	if !identity && (!graphUUID(values["job_id"]) || !graphUUID(values["schedule_id"])) {
		return false
	}
	if identity && ((values["job_id"] != "" && !graphUUID(values["job_id"])) || (values["schedule_id"] != "" && !graphUUID(values["schedule_id"]))) {
		return false
	}
	_, err := time.Parse("2006-01-02T15:04:05", values["deadline"])
	return err == nil && len(values["expected_identity"]) <= 512 && len(values["expected_start"]) <= 19
}

// EnableJobMutation adds only the existing selected-job cancel/start contracts.
func (b *Bridge) EnableJobMutation() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.session != "" || !b.jobRead || b.documentReadCutoff == nil {
		return errors.New("EPF job mutation configuration refused")
	}
	b.jobMutation = true
	return nil
}

// PinJobMutation requires the exact installed native interface before consent/work.
func (b *Bridge) PinJobMutation(ctx context.Context, retry bool) (context.Context, error) {
	pinned, err := b.PinJobRead(ctx)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.jobMutation {
		return nil, ErrCapabilityUnavailable
	}
	if b.core == "" || (!retry && b.jobControlContract != "job_control/1") || (retry && b.jobRetryContract != "job_retry/2") {
		return nil, ErrHybridUnavailable
	}
	return pinned, nil
}
func jobMutationCapability(cap string) bool {
	return cap == "pro.job-control" || cap == "pro.job-retry"
}
func jobCapability(cap string) bool { return jobReadCapability(cap) || jobMutationCapability(cap) }

func jobMutationBody(raw []byte, retry, cancel bool) bool {
	if !jobReadBody(raw, retry, false) {
		return false
	}
	var in struct {
		Identity    string `json:"expected_identity"`
		Start       string `json:"expected_start"`
		Composition string `json:"expected_composition"`
		Version     string `json:"expected_version_id"`
		Update      string `json:"expected_update_id"`
		Key         string `json:"key"`
		Run         string `json:"new_run_id"`
		RunStart    string `json:"new_run_start"`
		Base        string `json:"expected_base_token"`
	}
	if json.Unmarshal(raw, &in) != nil || in.Identity == "" {
		return false
	}
	if _, err := time.Parse("2006-01-02T15:04:05", in.Start); err != nil {
		return false
	}
	if !retry {
		return true
	}
	if !graphHex(in.Base) || !strings.HasPrefix(in.Key, "r24-") || !graphUUID(strings.TrimPrefix(in.Key, "r24-")) || !graphUUID(in.Version) || !graphUUID(in.Update) || in.Composition == "" || len(in.Composition) > 16384 {
		return false
	}
	if cancel {
		_, err := time.Parse("2006-01-02T15:04:05", in.RunStart)
		return graphUUID(in.Run) && err == nil
	}
	return in.Run == "" && in.RunStart == ""
}
