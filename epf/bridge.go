// Package epf connects a single explicitly paired native worker to existing 1C tools.
package epf

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const Protocol = 1
const MaxBody = 2 << 20
const maxPending = 16

var ErrCapabilityUnavailable = errors.New("EPF capability unavailable; use a supported connector read route or configure the required source")

var ErrSessionChanged = errors.New("EPF paired native session changed; start a new document read in the current connection; nothing was replayed")

var ErrHybridUnavailable = errors.New("EPF hybrid core unavailable; ask the administrator to install or update MCP_HTTPService and reconnect; FREE reads remain available")

var ErrUnavailable = errors.New("EPF connection unavailable; open the connector and pair it again")

// Task capabilities are assigned by the server, never by caller-supplied BSL.
// ExecuteBefore bounds admission, not native cancellation. CollectBefore permits late results.
type Task struct {
	ID            string          `json:"id"`
	Core          string          `json:"core,omitempty"`
	Session       string          `json:"session,omitempty"`
	Capability    string          `json:"capability"`
	Method        string          `json:"method"`
	Path          string          `json:"path"`
	Body          json.RawMessage `json:"body,omitempty"`
	BodyText      string          `json:"body_text,omitempty"`
	ObjectType    string          `json:"object_type,omitempty"`
	ObjectName    string          `json:"object_name,omitempty"`
	ExecuteBefore time.Time       `json:"execute_before"`
	CollectBefore time.Time       `json:"collect_before"`
}
type result struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}
type pending struct {
	task              Task
	session, identity string
	handed            bool
	result            chan result
	origin            context.Context
	terminal          *result
}

// UnknownOutcome is an opaque observation ticket for the original caller, never replay authority.
// shortcut: tickets are process-local; durable hybrid recovery needs its native receipt contract.
type UnknownOutcome struct {
	bridge  *Bridge
	request *pending
	cause   error
}

func (u *UnknownOutcome) Error() string {
	return "EPF native completion is unknown; retain the original operation for observation; no retry was sent"
}
func (u *UnknownOutcome) Unwrap() error { return u.cause }

var ErrOutcomeUnavailable = errors.New("EPF outcome unavailable or retention expired; do not replay the operation")

// Observe returns a copy of a terminal outcome only to its original opaque owner ticket.
func (b *Bridge) Observe(u *UnknownOutcome) (int, json.RawMessage, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prune(time.Now())
	if u == nil || u.bridge != b || u.request == nil || b.pending[u.request.task.ID] != u.request {
		return 0, nil, false, ErrOutcomeUnavailable
	}
	if u.request.terminal == nil {
		return 0, nil, false, nil
	}
	out := u.request.terminal
	return out.Status, bytes.Clone(out.Body), true, nil
}
func (b *Bridge) interrupted(p *pending, cause error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !p.handed {
		if b.pending[p.task.ID] == p {
			delete(b.pending, p.task.ID)
		}
		return cause
	}
	return &UnknownOutcome{bridge: b, request: p, cause: cause}
}

type Bridge struct {
	endpoint                   string
	codeExecuteContract        string
	codeExecute                bool
	mu                         sync.Mutex
	pairing, session, identity string
	pairingExpires             time.Time
	codeFile                   *os.File
	codePath                   string
	core                       string
	queryPackageContract       string
	objectDiffContract         string
	objectDiff                 bool
	jobDiagnoseContract        string
	jobDiagnose                bool
	incidentContract           string
	incident                   bool
	printExportContract        string
	printExport                bool
	reportComposeContract      string
	reportCompose              bool
	jobRead                    bool
	jobMutation                bool
	jobControlContract         string
	jobRetryContract           string
	jobControlReadContract     string
	jobRetryReadContract       string
	qualityContract            string
	quality                    bool
	reconcileContract          string
	reconcile                  bool
	exchangeQueueContract      string
	exchangeQueue              bool
	documentReadCutoff         func(context.Context) (time.Time, error)
	queryPackageCutoff         func(context.Context) (time.Time, error)
	documentDateWrite          bool
	graph                      bool
	graphContract              string
	pending                    map[string]*pending
	queue                      chan *pending
	closed                     bool
	started                    bool
	webOrigin                  string
	done                       chan struct{}
	// admit is rechecked on handout; result collection deliberately does not call it.
	admit func(context.Context, Task) error
}

func New(pairingToken string, admit func(context.Context, Task) error, webOrigin ...string) (*Bridge, error) {
	origin := ""
	if len(webOrigin) > 1 {
		return nil, errors.New("configure only one EPF web origin")
	}
	if len(webOrigin) == 1 {
		origin = webOrigin[0]
	}
	if err := validateWebOrigin(origin); err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(pairingToken)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("EPF pairing token must be 64 hexadecimal characters from a private file")
	}
	return &Bridge{webOrigin: origin, pairing: pairingToken, pairingExpires: time.Now().Add(pairingLifetime), pending: make(map[string]*pending), queue: make(chan *pending, maxPending), done: make(chan struct{}), admit: admit}, nil
}

// ReadToken refuses symlinks and shared files; it never includes file contents in errors.
func ReadToken(path string) (string, error) {
	f, err := openPrivateToken(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 66))
	if err != nil {
		return "", errors.New("EPF token file cannot be read")
	}
	token := strings.TrimSpace(string(data))
	if _, err = New(token, nil); err != nil {
		return "", err
	}
	return token, nil
}
func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func matches(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func bearer(r *http.Request) string {
	if len(r.Header.Values("Authorization")) != 1 || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}
func decode(w http.ResponseWriter, r *http.Request, out any, allowed ...string) bool {
	return decodeLimit(w, r, out, MaxBody, allowed...)
}
func decodeLimit(w http.ResponseWriter, r *http.Request, out any, limit int64, allowed ...string) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		http.Error(w, "EPF envelope too large", 413)
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	first, err := d.Token()
	valid := err == nil && utf8.Valid(raw) && first == json.Delim('{')
	seen := make(map[string]bool)
	for valid && d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] || !slices.Contains(allowed, name) {
			valid = false
			break
		}
		seen[name] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			valid = false
		}
	}
	if valid {
		last, err := d.Token()
		valid = err == nil && last == json.Delim('}') && d.Decode(new(any)) == io.EOF
	}
	if !valid {
		http.Error(w, "invalid EPF envelope", 400)
		return false
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		http.Error(w, "invalid EPF envelope", 400)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func (b *Bridge) prune(now time.Time) {
	for id, p := range b.pending {
		if !now.Before(p.task.CollectBefore) {
			delete(b.pending, id)
		}
	}
}

// validateWebOrigin accepts one canonical trusted origin; HTTP is local-only.
func validateWebOrigin(origin string) error {
	if origin == "" {
		return nil
	}
	u, err := url.Parse(origin)
	if err == nil && u.Host != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.RawFragment == "" && u.Opaque == "" && u.String() == origin && strings.ToLower(u.Host) == u.Host && !strings.ContainsAny(origin, "*, \t\r\n\\") {
		host := u.Hostname()
		if host == "" {
			return errors.New("EPF web origin requires a host")
		}
		portOK := true
		if u.Port() != "" {
			n, e := strconv.Atoi(u.Port())
			portOK = e == nil && n > 0 && n <= 65535 && strconv.Itoa(n) == u.Port() && !(u.Scheme == "https" && n == 443) && !(u.Scheme == "http" && n == 80)
		}
		if strings.HasSuffix(u.Host, ":") {
			portOK = false
		}
		ip := net.ParseIP(host)
		if strings.HasPrefix(u.Host, "[") && ip == nil {
			portOK = false
		}
		if ip != nil {
			if ip.String() != host {
				portOK = false
			}
		} else {
			for _, ch := range host {
				if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '.') {
					portOK = false
				}
			}
			if strings.Contains(host, ":") || strings.Trim(host, "0123456789.") == "" {
				portOK = false
			}
		}
		local := host == "localhost" || (ip != nil && ip.IsLoopback())
		if portOK && (u.Scheme == "https" || (u.Scheme == "http" && local)) {
			return nil
		}
	}
	return errors.New("EPF web origin must be one canonical HTTPS origin or local HTTP origin, without path, credentials or wildcards")
}

func preflightMethod(path string) string {
	switch path {
	case "/pair", "/poll":
		return "POST"
	case "/session":
		return "DELETE"
	}
	if strings.HasPrefix(path, "/result/") {
		id := strings.TrimPrefix(path, "/result/")
		if raw, err := hex.DecodeString(id); err == nil && len(raw) == 16 {
			return "POST"
		}
	}
	return ""
}

// Start binds only a literal loopback address.
func (b *Bridge) Start(address string) (func(), error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		b.Close()
		return nil, errors.New("EPF listener requires 127.0.0.1:PORT")
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		b.Close()
		return nil, errors.New("EPF listener cannot bind the configured address")
	}
	b.mu.Lock()
	if b.closed || b.started {
		b.mu.Unlock()
		ln.Close()
		return nil, ErrUnavailable
	}
	if b.codeFile != nil {
		if err := b.issueCode(); err != nil {
			b.mu.Unlock()
			ln.Close()
			b.Close()
			return nil, err
		}
	}
	b.started = true
	b.mu.Unlock()
	server := &http.Server{Handler: b.Handler(ln.Addr().String()), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	go func() { _ = server.Serve(ln) }()
	return func() { b.Close(); _ = server.Close() }, nil
}
func (b *Bridge) Handler(host string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Host != host || r.URL.RawQuery != "" || r.URL.ForceQuery {
			http.Error(w, "EPF origin refused", 403)
			return
		}
		origins, hasOrigin := r.Header["Origin"]
		if hasOrigin {
			if len(origins) != 1 || b.webOrigin == "" || origins[0] != b.webOrigin {
				http.Error(w, "EPF origin refused", 403)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", b.webOrigin)
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == "OPTIONS" {
			method := preflightMethod(r.URL.Path)
			methods := r.Header.Values("Access-Control-Request-Method")
			headers := r.Header.Values("Access-Control-Request-Headers")
			valid := hasOrigin && method != "" && len(methods) == 1 && methods[0] == method && len(headers) <= 1
			if len(headers) == 1 {
				for _, name := range strings.Split(headers[0], ",") {
					name = strings.TrimSpace(name)
					if !strings.EqualFold(name, "Authorization") && !strings.EqualFold(name, "Content-Type") {
						valid = false
					}
				}
			}
			if !valid {
				http.Error(w, "EPF preflight refused", 403)
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", method)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == "POST" && r.URL.Path == "/pair" {
			b.pair(w, r)
			return
		}
		if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/result/") {
			b.complete(w, r)
			return
		}
		b.mu.Lock()
		valid := !b.closed && matches(b.session, bearer(r))
		b.mu.Unlock()
		if !valid {
			http.Error(w, "EPF session refused", 401)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/poll":
			b.poll(w, r)
		case r.Method == "DELETE" && r.URL.Path == "/session":
			b.mu.Lock()
			if !b.closed && matches(b.session, bearer(r)) {
				b.session = ""
				b.identity = ""
				b.core = ""
				b.queryPackageContract = ""
				b.objectDiffContract = ""
				b.jobDiagnoseContract = ""
				b.incidentContract = ""
				b.exchangeQueueContract = ""
				b.reconcileContract = ""
				b.qualityContract = ""
				b.jobControlReadContract = ""
				b.jobControlContract = ""
				b.jobRetryContract = ""
				b.jobRetryReadContract = ""
				b.reportComposeContract = ""
				b.printExportContract = ""
				b.codeExecuteContract = ""
				if err := b.issueCode(); err != nil {
					b.mu.Unlock()
					http.Error(w, err.Error(), 503)
					return
				}
			} else {
				b.mu.Unlock()
				http.Error(w, "EPF session refused", 401)
				return
			}
			b.mu.Unlock()
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	})
}

// shortcut: one local worker uses explicit stop/restart; extend only for a required verified multi-worker lifecycle.
func (b *Bridge) pair(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	valid := !b.closed && matches(b.pairing, bearer(r)) && time.Now().Before(b.pairingExpires)
	if valid && b.codeFile != nil {
		valid = validateCodeFile(b.codeFile, b.codePath) == nil
	}
	b.mu.Unlock()
	if !valid {
		http.Error(w, "EPF pairing refused", 401)
		return
	}
	var in struct {
		Protocol       int             `json:"protocol"`
		Identity       string          `json:"identity"`
		Core           string          `json:"core,omitempty"`
		QueryPackage   json.RawMessage `json:"query_package,omitempty"`
		ObjectDiff     json.RawMessage `json:"object_diff,omitempty"`
		JobDiagnose    json.RawMessage `json:"job_diagnose,omitempty"`
		Incident       json.RawMessage `json:"incident_timeline,omitempty"`
		ExchangeQueue  json.RawMessage `json:"read_exchange_queue,omitempty"`
		Reconcile      json.RawMessage `json:"reconcile,omitempty"`
		Quality        json.RawMessage `json:"quality,omitempty"`
		JobControlRead json.RawMessage `json:"job_control_read,omitempty"`
		JobControl     json.RawMessage `json:"job_control,omitempty"`
		JobRetry       json.RawMessage `json:"job_retry,omitempty"`
		JobRetryRead   json.RawMessage `json:"job_retry_read,omitempty"`
		ReportCompose  json.RawMessage `json:"report_compose,omitempty"`
		PrintExport    json.RawMessage `json:"print_export,omitempty"`
		CodeExecute    json.RawMessage `json:"code_execute,omitempty"`
		Graph          json.RawMessage `json:"graph,omitempty"`
	}
	if !decode(w, r, &in, "protocol", "identity", "core", "query_package", "object_diff", "job_diagnose", "incident_timeline", "read_exchange_queue", "reconcile", "quality", "report_compose", "print_export", "code_execute", "graph", "job_control_read", "job_retry_read", "job_control", "job_retry") {
		return
	}
	if in.Protocol != Protocol || strings.TrimSpace(in.Identity) != in.Identity || in.Identity == "" || len(in.Identity) > 1024 || len(in.Core) > 4096 {
		http.Error(w, "EPF identity/protocol refused", 400)
		return
	}
	var packageContract string
	if len(in.QueryPackage) != 0 && (json.Unmarshal(in.QueryPackage, &packageContract) != nil || packageContract != "query_package/1" || in.Core == "") {
		http.Error(w, "EPF package contract refused", 400)
		return
	}
	var objectDiffContract string
	if len(in.ObjectDiff) != 0 && (json.Unmarshal(in.ObjectDiff, &objectDiffContract) != nil || objectDiffContract != "object_diff/1" || in.Core == "") {
		http.Error(w, "EPF object diff contract refused", 400)
		return
	}
	var jobDiagnoseContract string
	if len(in.JobDiagnose) != 0 && (json.Unmarshal(in.JobDiagnose, &jobDiagnoseContract) != nil || jobDiagnoseContract != "JOB-DIAG-1" || in.Core == "") {
		http.Error(w, "EPF job diagnosis contract refused", 400)
		return
	}

	var incidentContract string
	if len(in.Incident) != 0 && (json.Unmarshal(in.Incident, &incidentContract) != nil || incidentContract != "INCIDENT-TIMELINE-1" || in.Core == "") {
		http.Error(w, "EPF incident contract refused", 400)
		return
	}

	var exchangeQueueContract string
	if len(in.ExchangeQueue) != 0 && (json.Unmarshal(in.ExchangeQueue, &exchangeQueueContract) != nil || exchangeQueueContract != "queue_observation/1" || in.Core == "") {
		http.Error(w, "EPF exchange queue contract refused", 400)
		return
	}
	var reconcileContract string
	if len(in.Reconcile) != 0 && (json.Unmarshal(in.Reconcile, &reconcileContract) != nil || reconcileContract != "reconcile_read/1" || in.Core == "") {
		http.Error(w, "EPF reconcile contract refused", 400)
		return
	}
	var jobControlContract, jobRetryContract string
	if len(in.JobControl) != 0 && (json.Unmarshal(in.JobControl, &jobControlContract) != nil || jobControlContract != "job_control/1" || in.Core == "") {
		http.Error(w, "EPF job control contract refused", 400)
		return
	}
	if len(in.JobRetry) != 0 && (json.Unmarshal(in.JobRetry, &jobRetryContract) != nil || jobRetryContract != "job_retry/2" || in.Core == "") {
		http.Error(w, "EPF job retry contract refused", 400)
		return
	}
	var jobControlReadContract, jobRetryReadContract string
	if len(in.JobControlRead) != 0 && (json.Unmarshal(in.JobControlRead, &jobControlReadContract) != nil || jobControlReadContract != "job_control_read/1" || in.Core == "") {
		http.Error(w, "EPF job control read contract refused", 400)
		return
	}
	if len(in.JobRetryRead) != 0 && (json.Unmarshal(in.JobRetryRead, &jobRetryReadContract) != nil || jobRetryReadContract != "job_retry_read/2" || in.Core == "") {
		http.Error(w, "EPF job retry read contract refused", 400)
		return
	}
	var qualityContract string
	if len(in.Quality) != 0 && (json.Unmarshal(in.Quality, &qualityContract) != nil || qualityContract != "quality_read/1" || in.Core == "") {
		http.Error(w, "EPF quality contract refused", 400)
		return
	}
	var reportComposeContract string
	if len(in.ReportCompose) != 0 && (json.Unmarshal(in.ReportCompose, &reportComposeContract) != nil || reportComposeContract != "report_compose/1" || in.Core == "") {
		http.Error(w, "EPF report composition contract refused", 400)
		return
	}
	var codeExecuteContract string
	if len(in.CodeExecute) != 0 && (json.Unmarshal(in.CodeExecute, &codeExecuteContract) != nil || codeExecuteContract != "code_execute/1" || in.Core == "") {
		http.Error(w, "EPF code execution contract refused", 400)
		return
	}
	var printExportContract string
	if len(in.PrintExport) != 0 && (json.Unmarshal(in.PrintExport, &printExportContract) != nil || printExportContract != "print_export/1" || in.Core == "") {
		http.Error(w, "EPF print/export contract refused", 400)
		return
	}
	var graphContract string
	if len(in.Graph) != 0 && (json.Unmarshal(in.Graph, &graphContract) != nil || graphContract != "graph_transfer/1" || in.Core == "") {
		http.Error(w, "EPF graph contract refused", 400)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || !matches(b.pairing, bearer(r)) || !time.Now().Before(b.pairingExpires) || (b.codeFile != nil && validateCodeFile(b.codeFile, b.codePath) != nil) {
		http.Error(w, "EPF pairing refused; restart MCP and read its fresh five-minute code", 401)
		return
	}
	if b.session != "" {
		http.Error(w, "EPF already paired; stop the existing session first", 409)
		return
	}
	b.pairing = ""
	b.pairingExpires = time.Time{}
	b.session = randomToken(32)
	b.identity = in.Identity
	b.core = in.Core
	b.queryPackageContract = packageContract
	b.objectDiffContract = objectDiffContract
	b.jobDiagnoseContract = jobDiagnoseContract
	b.incidentContract = incidentContract
	b.exchangeQueueContract = exchangeQueueContract
	b.reconcileContract = reconcileContract
	b.qualityContract = qualityContract
	b.jobControlReadContract = jobControlReadContract
	b.jobControlContract = jobControlContract
	b.jobRetryContract = jobRetryContract
	b.jobRetryReadContract = jobRetryReadContract
	b.reportComposeContract = reportComposeContract
	b.printExportContract = printExportContract
	b.codeExecuteContract = codeExecuteContract
	b.graphContract = graphContract
	writeJSON(w, struct {
		Protocol     int      `json:"protocol"`
		Session      string   `json:"session"`
		Identity     string   `json:"identity"`
		Capabilities []string `json:"capabilities"`
	}{Protocol, b.session, b.identity, []string{"community.metadata", "community.configuration", "community.query", "community.version", "community.object", "community.subsystems", "community.form", "community.validate-query", "community.count", "community.storage", "community.eventlog"}})
}
func (b *Bridge) poll(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Identity string `json:"identity"`
	}
	if !decode(w, r, &in, "identity") {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prune(time.Now())
	if !matches(b.session, bearer(r)) || in.Identity != b.identity {
		http.Error(w, "EPF native identity changed", 409)
		return
	}
	for range maxPending {
		select {
		case p := <-b.queue:
			if b.pending[p.task.ID] != p || p.session != b.session {
				continue
			}
			if p.origin.Err() != nil || !time.Now().Before(p.task.ExecuteBefore) {
				delete(b.pending, p.task.ID)
				continue
			}
			if ((p.task.Capability == "advanced.code-execute" || p.task.Capability == "advanced.query-package" || p.task.Capability == "advanced.object-diff" || p.task.Capability == "advanced.job-diagnose") && !b.packageAdmitted(p.origin, &p.task)) || ((p.task.Capability == "pro.document-read" || p.task.Capability == "pro.document-date" || p.task.Capability == "pro.graph" || (jobCapability(p.task.Capability) || p.task.Capability == "pro.incident-context") || (p.task.Capability == "pro.exchange-queue" || (p.task.Capability == "pro.reconcile-read" || (p.task.Capability == "pro.quality-read" || (p.task.Capability == "pro.report-compose" || p.task.Capability == "pro.print-export"))))) && !b.readAdmitted(p.origin, &p.task)) || (b.admit != nil && b.admit(p.origin, p.task) != nil) {
				delete(b.pending, p.task.ID)
				p.result <- result{403, json.RawMessage(`{"error":"EPF task admission refused"}`)}
				continue
			}
			if p.origin.Err() != nil || !time.Now().Before(p.task.ExecuteBefore) {
				delete(b.pending, p.task.ID)
				continue
			}
			p.handed = true
			writeJSON(w, p.task)
			return
		default:
			w.WriteHeader(204)
			return
		}
	}
	w.WriteHeader(204)
}
func (b *Bridge) complete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/result/")
	b.mu.Lock()
	b.prune(time.Now())
	p := b.pending[id]
	valid := p != nil && p.handed && p.terminal == nil && matches(p.session, bearer(r))
	b.mu.Unlock()
	if !valid {
		http.Error(w, "EPF result binding refused", 409)
		return
	}
	var in struct {
		Identity string          `json:"identity"`
		Status   int             `json:"status"`
		Body     json.RawMessage `json:"body"`
		BodyText *string         `json:"body_text"`
	}
	allowed := []string{"identity", "status", "body"}
	if p.task.Capability == "advanced.code-execute" || p.task.Capability == "advanced.query-package" || p.task.Capability == "advanced.object-diff" || p.task.Capability == "advanced.job-diagnose" || (jobCapability(p.task.Capability) || p.task.Capability == "pro.incident-context") || (p.task.Capability == "pro.exchange-queue" || (p.task.Capability == "pro.reconcile-read" || (p.task.Capability == "pro.quality-read" || (p.task.Capability == "pro.report-compose" || p.task.Capability == "pro.print-export")))) {
		allowed = append(allowed, "body_text")
	}
	rawLimit, envelopeLimit := 1048576, MaxBody
	if jobCapability(p.task.Capability) {
		rawLimit = 32768
		envelopeLimit = 2*rawLimit + (16 << 10)
	}
	if p.task.Capability == "pro.reconcile-read" {
		rawLimit += 16 << 10
		envelopeLimit = 2*rawLimit + (16 << 10)
	}
	if p.task.Capability == "pro.report-compose" {
		rawLimit = 8 << 20
		envelopeLimit = 2*rawLimit + (16 << 10)
	}
	if p.task.Capability == "pro.print-export" && printExportFileBody([]byte(p.task.BodyText)) {
		rawLimit = 12 << 20
		envelopeLimit = 2*rawLimit + (16 << 10)
	}
	if !decodeLimit(w, r, &in, int64(envelopeLimit), allowed...) {
		return
	}
	if (p.task.Capability == "advanced.code-execute" || p.task.Capability == "advanced.query-package" || p.task.Capability == "advanced.object-diff" || p.task.Capability == "advanced.job-diagnose" || (jobCapability(p.task.Capability) || p.task.Capability == "pro.incident-context") || (p.task.Capability == "pro.exchange-queue" || (p.task.Capability == "pro.reconcile-read" || (p.task.Capability == "pro.quality-read" || (p.task.Capability == "pro.report-compose" || p.task.Capability == "pro.print-export"))))) && in.Status == 200 {
		if in.BodyText == nil || len(in.Body) != 0 || len(*in.BodyText) > rawLimit {
			http.Error(w, "EPF native package envelope refused", 400)
			return
		}
		in.Body = json.RawMessage(*in.BodyText)
	} else if in.BodyText != nil {
		http.Error(w, "EPF result envelope refused", 400)
		return
	}
	if p.task.Capability == "pro.print-export" && len(in.Body) > rawLimit {
		http.Error(w, "EPF print/export raw result refused", 400)
		return
	}
	if in.Identity != p.identity || in.Status < 200 || in.Status > 599 || len(in.Body) == 0 || !json.Valid(in.Body) {
		http.Error(w, "EPF result envelope refused", 400)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending[id] != p || p.terminal != nil || !time.Now().Before(p.task.CollectBefore) {
		http.Error(w, "EPF result no longer outstanding", 409)
		return
	}
	// Collection is independent of current entitlement and execution deadline.
	p.terminal = &result{in.Status, in.Body}
	p.result <- *p.terminal
	w.WriteHeader(204)
}
func capability(method, path string) string {
	switch method + " " + path {
	case "GET /metadata":
		return "community.metadata"
	case "GET /configuration":
		return "community.configuration"
	case "GET /version":
		return "community.version"
	case "POST /query":
		return "community.query"
	case "POST /validate-query":
		return "community.validate-query"
	case "POST /count":
		return "community.count"
	case "POST /eventlog":
		return "community.eventlog"
	case "GET /subsystems":
		return "community.subsystems"
	}
	if method == http.MethodGet {
		route, _, _ := metadataRoute(path)
		if route != "" {
			return "community." + route
		}
	}
	return ""
}

// metadataRoute accepts only fixed kinds and metadata identifiers, never a native URL or BSL.
func metadataRoute(path string) (route, kind, name string) {
	parts := strings.Split(path, "/")
	if len(parts) != 4 || (parts[1] != "object" && parts[1] != "form" && parts[1] != "storage") {
		return "", "", ""
	}
	kind = parts[2]
	switch kind {
	case "Catalog", "Document", "Enum", "DataProcessor", "Report", "InformationRegister", "AccumulationRegister", "AccountingRegister", "CalculationRegister", "ChartOfAccounts", "ChartOfCharacteristicTypes", "ChartOfCalculationTypes", "ExchangePlan", "BusinessProcess", "Task", "DefinedType", "Subsystem":
	default:
		return "", "", ""
	}
	if parts[1] == "storage" && kind == "Subsystem" {
		return "", "", ""
	}
	name, err := url.PathUnescape(parts[3])
	if err != nil || !utf8.ValidString(name) || len(name) > 1024 {
		return "", "", ""
	}
	segments := strings.Split(name, ".")
	if kind != "Subsystem" && len(segments) != 1 {
		return "", "", ""
	}
	for _, segment := range segments {
		if segment == "" || utf8.RuneCountInString(segment) > 128 {
			return "", "", ""
		}
		for i, c := range segment {
			if c != '_' && !unicode.IsLetter(c) && (i == 0 || !unicode.IsDigit(c)) {
				return "", "", ""
			}
		}
	}
	return parts[1], kind, name
}

// UseContextTimeout retains opaque outcomes through the client's configured deadline.
// Transport wrappers must preserve this opt-in when used with onec.Client.
func (b *Bridge) UseContextTimeout() bool { return true }

func (b *Bridge) RoundTrip(r *http.Request) (*http.Response, error) {
	if b.endpoint != "" && (r.URL == nil || r.URL.Scheme+"://"+r.URL.Host != b.endpoint || r.URL.User != nil || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.Fragment != "" || r.URL.RawPath != "" || r.URL.Opaque != "") {
		return nil, errors.New("EPF base endpoint mismatch; select the configured base name")
	}

	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	cap := capability(r.Method, r.URL.EscapedPath())
	if r.Method == "POST" && r.URL.EscapedPath() == "/execute" && b.codeExecute {
		cap = "advanced.code-execute"
	}
	if r.Method == "POST" && r.URL.EscapedPath() == "/print-export" && b.printExport {
		cap = "pro.print-export"
	}
	if r.Method == "POST" && r.URL.EscapedPath() == "/report-compose" && b.reportCompose {
		cap = "pro.report-compose"
	}
	if r.Method == "POST" && r.URL.EscapedPath() == "/quality-read" && b.quality {
		cap = "pro.quality-read"
	}
	if r.Method == "POST" && r.URL.EscapedPath() == "/reconcile-read" && b.reconcile {
		cap = "pro.reconcile-read"
	}
	if r.Method == "POST" && r.URL.EscapedPath() == "/query-package" && b.queryPackageCutoff != nil {
		cap = "advanced.query-package"
	}
	if r.Method == "POST" && r.URL.EscapedPath() == "/incident-context" && b.incident {
		cap = "pro.incident-context"
	}
	if r.Method == "POST" && r.URL.EscapedPath() == "/job-diagnose" && b.jobDiagnose {
		cap = "advanced.job-diagnose"
	}
	if r.Method == "POST" && r.URL.EscapedPath() == "/object-diff" && b.objectDiff {
		cap = "advanced.object-diff"
	}
	if r.Method == "POST" && r.URL.EscapedPath() == "/document-read" && b.documentReadCutoff != nil {
		cap = "pro.document-read"
	}
	// Advanced code observation owns only the exact native technical identity lane.
	if r.Method == "POST" && r.URL.EscapedPath() == "/document-read" && b.codeExecute && r.Context().Value(ownerObservationKey{}) == true {
		cap = "advanced.code-owner-identity"
	}
	if r.Method == "POST" && b.documentDateWrite && r.URL.EscapedPath() == "/document-commit" {
		cap = "pro.document-date"
	}
	if r.Method == "POST" && b.jobRead {
		switch r.URL.EscapedPath() {
		case "/job-control-read":
			cap = "pro.job-control-read"
		case "/job-retry-identity", "/job-retry-read":
			cap = "pro.job-retry-read"
		}
	}
	if r.Method == "POST" && b.jobMutation {
		switch r.URL.EscapedPath() {
		case "/job-control-cancel":
			cap = "pro.job-control"
		case "/job-retry-start", "/job-retry-cancel":
			cap = "pro.job-retry"
		}
	}
	if cap == "" || r.URL.RawQuery != "" {
		return nil, ErrCapabilityUnavailable
	}
	var body []byte
	if r.Body != nil {
		var err error
		limit := 65536
		if b.documentDateWrite && (cap == "pro.document-read" || cap == "pro.document-date" || cap == "pro.graph") {
			limit = documentWriteEnvelopeLimit
		}
		if cap == "advanced.job-diagnose" || cap == "pro.incident-context" {
			limit = 8192
		}
		if jobCapability(cap) {
			limit = 32768
		}
		if cap == "advanced.object-diff" {
			limit = 32768
		}
		if cap == "advanced.query-package" {
			limit = 524288
		}
		if cap == "pro.reconcile-read" {
			limit = 1064960
		}
		if cap == "pro.quality-read" {
			limit = 32768
		}
		if cap == "pro.report-compose" {
			limit = 1 << 20
		}
		body, err = io.ReadAll(io.LimitReader(r.Body, int64(limit+1)))
		_ = r.Body.Close()
		if err != nil || len(body) > limit || !json.Valid(body) {
			return nil, errors.New("EPF request body refused")
		}
	}
	if cap == "advanced.code-owner-identity" {
		if !documentOwnerIdentityBody(body) {
			return nil, ErrCapabilityUnavailable
		}
		cap = "pro.document-receipt"
	}
	if jobCapability(cap) && !jobReadBody(body, (cap == "pro.job-retry-read" || cap == "pro.job-retry"), r.URL.EscapedPath() == "/job-retry-identity") {
		return nil, errors.New("EPF job read request refused")
	}
	if jobMutationCapability(cap) && !jobMutationBody(body, cap == "pro.job-retry", r.URL.EscapedPath() == "/job-retry-cancel") {
		return nil, errors.New("EPF job mutation request refused")
	}
	if cap == "advanced.code-execute" && !codeExecuteBody(body) {
		return nil, errors.New("EPF code execution request refused")
	}
	if cap == "pro.print-export" && !printExportBody(body) {
		return nil, errors.New("EPF print/export request refused")
	}
	if cap == "pro.report-compose" && !reportComposeBody(body) {
		return nil, errors.New("EPF report composition request refused")
	}
	if cap == "pro.quality-read" && !qualityBody(body) {
		return nil, errors.New("EPF quality request refused")
	}
	if cap == "pro.reconcile-read" && !reconcileBody(body) {
		return nil, errors.New("EPF reconcile request refused")
	}
	if cap == "pro.incident-context" && !incidentBody(body) {
		return nil, errors.New("EPF incident request refused")
	}
	if cap == "advanced.job-diagnose" && !jobDiagnoseBody(body) {
		return nil, errors.New("EPF job diagnosis request refused")
	}
	if cap == "advanced.object-diff" && !objectDiffBody(body) {
		return nil, errors.New("EPF object diff request refused")
	}
	if cap == "advanced.query-package" && !queryPackageBody(body) {
		return nil, errors.New("EPF query package request refused")
	}
	if b.graph && ((cap == "pro.document-read" && graphBody(body, false, false)) || (cap == "pro.document-date" && graphBody(body, true, false))) {
		cap = "pro.graph"
	}
	if b.graph && cap == "pro.document-read" && graphBody(body, false, true) {
		cap = "pro.graph-receipt"
	}
	if cap == "pro.document-read" && exchangeQueueBody(body) {
		if !b.exchangeQueue {
			return nil, ErrCapabilityUnavailable
		}
		if len(body) > 8192 {
			return nil, errors.New("EPF request body refused")
		}
		cap = "pro.exchange-queue"
	}
	// Only technical identity uses the existing read-only observation lane.
	if cap == "pro.document-read" && b.documentDateWrite && r.Context().Value(ownerObservationKey{}) == true && documentOwnerIdentityBody(body) {
		cap = "pro.document-receipt"
	}
	if cap == "pro.document-read" && !documentReadBody(body) {
		if !b.documentDateWrite {
			return nil, errors.New("EPF document request refused")
		}
		if documentDateBody(body, false, true) {
			cap = "pro.document-receipt"
		} else if documentDateBody(body, false, false) {
			cap = "pro.document-date"
		} else {
			return nil, errors.New("EPF document request refused")
		}
	}
	if cap == "pro.document-date" && r.URL.EscapedPath() != "/document-read" {
		if !documentDateBody(body, true, false) {
			return nil, errors.New("EPF document Date request refused")
		}
	}
	if cap == "pro.document-read" && len(body) > 65536 {
		return nil, errors.New("EPF request body refused")
	}
	now := time.Now()
	deadline := now.Add(30 * time.Second)
	if cap == "advanced.code-execute" {
		var in struct {
			Timeout int `json:"timeout"`
		}
		_ = json.Unmarshal(body, &in) // The closed wire body was validated above.
		deadline = now.Add(time.Duration(in.Timeout) * time.Second)
	}
	if d, ok := r.Context().Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	executeDeadline := deadline
	if cap == "advanced.code-execute" && now.Add(30*time.Second).Before(executeDeadline) {
		executeDeadline = now.Add(30 * time.Second)
	}
	if cap == "pro.document-read" || cap == "pro.document-date" || cap == "pro.graph" || (jobCapability(cap) || cap == "pro.incident-context") || (cap == "pro.exchange-queue" || (cap == "pro.reconcile-read" || (cap == "pro.quality-read" || (cap == "pro.report-compose" || cap == "pro.print-export")))) || (cap == "advanced.code-execute" || cap == "advanced.query-package" || cap == "advanced.object-diff" || cap == "advanced.job-diagnose") {
		check := b.documentReadCutoff
		if cap == "advanced.code-execute" || cap == "advanced.query-package" || cap == "advanced.object-diff" || cap == "advanced.job-diagnose" {
			check = b.queryPackageCutoff
		}
		cutoff, err := check(r.Context())
		if err != nil {
			return nil, err
		}
		if cutoff.Before(executeDeadline) {
			executeDeadline = cutoff
		}
	}
	if cap == "pro.document-read" || cap == "pro.document-date" || cap == "pro.graph" {
		if cutoff, ok := r.Context().Value(documentCutoffKey{}).(time.Time); ok && cutoff.Before(executeDeadline) {
			executeDeadline = cutoff
		}
	}

	if cap == "advanced.code-execute" {
		if cutoff, ok := r.Context().Value(codeExecuteCutoffKey{}).(time.Time); ok && cutoff.Before(executeDeadline) {
			executeDeadline = cutoff
		}
	}
	if cap == "pro.reconcile-read" {
		if cutoff, ok := r.Context().Value(reconcileCutoffKey{}).(time.Time); ok && cutoff.Before(executeDeadline) {
			executeDeadline = cutoff
		}
	}
	if jobCapability(cap) {
		if cutoff, ok := r.Context().Value(jobReadCutoffKey{}).(time.Time); ok && cutoff.Before(executeDeadline) {
			executeDeadline = cutoff
		}
	}
	if cap == "pro.quality-read" {
		if cutoff, ok := r.Context().Value(qualityCutoffKey{}).(time.Time); ok && cutoff.Before(executeDeadline) {
			executeDeadline = cutoff
		}
	}
	if cap == "pro.report-compose" {
		if cutoff, ok := r.Context().Value(reportComposeCutoffKey{}).(time.Time); ok && cutoff.Before(executeDeadline) {
			executeDeadline = cutoff
		}
	}
	if !executeDeadline.After(now) {
		return nil, context.DeadlineExceeded
	}
	b.mu.Lock()
	b.prune(now)
	if err := r.Context().Err(); err != nil {
		b.mu.Unlock()
		return nil, err
	}
	if b.closed || b.session == "" {
		b.mu.Unlock()
		return nil, ErrUnavailable
	}
	if pin, ok := r.Context().Value(sessionPinKey{}).(sessionPin); ok && (pin.bridge != b || pin.session != b.session || pin.identity != b.identity || pin.core != b.core) {
		b.mu.Unlock()
		return nil, ErrSessionChanged
	}

	if (cap == "pro.job-control" && b.jobControlContract != "job_control/1") || (cap == "pro.job-retry" && b.jobRetryContract != "job_retry/2") || (cap == "pro.job-control-read" && b.jobControlReadContract != "job_control_read/1") || (cap == "pro.job-retry-read" && b.jobRetryReadContract != "job_retry_read/2") || (cap == "advanced.code-execute" && b.codeExecuteContract != "code_execute/1") || (strings.HasPrefix(cap, "pro.document-") && b.core == "") || ((cap == "pro.graph" || cap == "pro.graph-receipt") && (b.core == "" || b.graphContract != "graph_transfer/1")) || (cap == "advanced.query-package" && b.queryPackageContract != "query_package/1") || (cap == "advanced.object-diff" && b.objectDiffContract != "object_diff/1") || (cap == "advanced.job-diagnose" && b.jobDiagnoseContract != "JOB-DIAG-1") || (cap == "pro.incident-context" && b.incidentContract != "INCIDENT-TIMELINE-1") || (cap == "pro.exchange-queue" && b.exchangeQueueContract != "queue_observation/1") || (cap == "pro.reconcile-read" && b.reconcileContract != "reconcile_read/1") || (cap == "pro.quality-read" && b.qualityContract != "quality_read/1") || (cap == "pro.report-compose" && b.reportComposeContract != "report_compose/1") || (cap == "pro.print-export" && b.printExportContract != "print_export/1") {
		b.mu.Unlock()
		return nil, ErrHybridUnavailable
	}
	if cap == "advanced.code-execute" || cap == "pro.report-compose" || cap == "pro.print-export" {
		for _, outstanding := range b.pending {
			if outstanding.task.Capability == cap && outstanding.terminal == nil {
				b.mu.Unlock()
				return nil, ErrUnavailable
			}
		}
	}
	if len(b.pending) >= maxPending || len(b.queue) >= maxPending {
		b.mu.Unlock()
		return nil, errors.New("EPF task limit reached")
	}
	_, objectType, objectName := metadataRoute(r.URL.EscapedPath())
	p := &pending{task: Task{ID: randomToken(16), Capability: cap, Method: r.Method, Path: r.URL.EscapedPath(), Body: body, ObjectType: objectType, ObjectName: objectName, ExecuteBefore: executeDeadline.UTC(), CollectBefore: deadline.Add(time.Minute).UTC()}, session: b.session, identity: b.identity, result: make(chan result, 1), origin: r.Context()}
	if cap == "pro.graph" || cap == "pro.graph-receipt" || strings.HasPrefix(cap, "pro.document-") || (jobCapability(cap) || cap == "pro.incident-context") || (cap == "pro.exchange-queue" || (cap == "pro.reconcile-read" || (cap == "pro.quality-read" || (cap == "pro.report-compose" || cap == "pro.print-export")))) || (cap == "advanced.code-execute" || cap == "advanced.query-package" || cap == "advanced.object-diff" || cap == "advanced.job-diagnose") {
		p.task.Core = b.core
		p.task.Session = b.session
	}
	if cap == "advanced.code-execute" || cap == "advanced.query-package" || cap == "advanced.object-diff" || cap == "advanced.job-diagnose" || (jobCapability(cap) || cap == "pro.incident-context") || (cap == "pro.exchange-queue" || (cap == "pro.reconcile-read" || (cap == "pro.quality-read" || (cap == "pro.report-compose" || cap == "pro.print-export")))) {
		p.task.BodyText = string(body)
		p.task.Body = nil
	}
	b.pending[p.task.ID] = p
	b.queue <- p
	b.mu.Unlock()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case out := <-p.result:
		b.mu.Lock()
		if b.pending[p.task.ID] == p {
			delete(b.pending, p.task.ID)
		}
		b.mu.Unlock()
		return &http.Response{StatusCode: out.Status, Status: http.StatusText(out.Status), Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(out.Body)), Request: r}, nil
	case <-b.done:
		return nil, b.interrupted(p, ErrUnavailable)
	case <-r.Context().Done():
		return nil, b.interrupted(p, r.Context().Err())
	case <-timer.C:
		return nil, b.interrupted(p, context.DeadlineExceeded)
	}
}
func (b *Bridge) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	b.pairing = ""
	b.pairingExpires = time.Time{}
	if b.codeFile != nil {
		b.codeFile.Close()
		b.codeFile = nil
	}
	close(b.done)
	b.session = ""
	b.identity = ""
}
