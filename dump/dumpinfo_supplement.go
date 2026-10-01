package dump

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"
)

const maxDumpInfoBytes = 1 << 20
const maxDumpInfoExtrasBytes = 64 << 10
const maxDumpInfoExtras = 64

// DumpInfoField is an additional dump.json member. Update copies Value; callers
// retain ownership of their input. Standard metadata names are reserved.
type DumpInfoField struct {
	Name  string
	Value json.RawMessage
}

// ErrDumpInfoUnavailable means no valid standard mapping exists yet. Update
// never creates an extra-only document; wait for the index's Done/Ready first.
var ErrDumpInfoUnavailable = errors.New("dump: valid standard dump.json is unavailable")

// DumpInfoMetadata coordinates supplemental metadata for one stable cache path.
// Keep it open while supplemental values should survive a removed dump.json.
// Indexes also retain this state until Close. Close the handle when finished;
// the last reference removes its registry entry. Coordination is process-local:
// other processes still use atomic replacement, but do not share this mutex.
type DumpInfoMetadata struct {
	mu     sync.RWMutex
	state  *dumpInfoState
	closed bool
}

type dumpInfoState struct {
	mu       sync.Mutex
	path     string
	dumpPath string // expected absolute owner identity; immutable while registered
	refs     int    // protected by dumpInfoRegistry
	extras   []DumpInfoField
}

var dumpInfoRegistry = struct {
	sync.Mutex
	paths map[string]*dumpInfoState
}{paths: make(map[string]*dumpInfoState)}

// OpenDumpInfoMetadata registers the SAME dump/cache-base pair used to open an
// Index. cacheDir is the cache base, never <hash> or g/<generation>. No mapping
// is created here. A missing cache base is created so physical base identity is
// established before registration; the hash directory remains absent. Update
// merges only additional members into CURRENT standard
// metadata, so delayed startup scans cannot restore an obsolete index snapshot.
func OpenDumpInfoMetadata(dumpDir, cacheDir string) (*DumpInfoMetadata, error) {
	cpath, err := cachePath(dumpDir, cacheDir)
	if err != nil {
		return nil, err
	}
	return openDumpInfoMetadata(cpath, dumpDir)
}

// Resolve existing ancestors too: cache hash directories may not exist yet,
// and /var, /private/var or a symlinked cache base must share one mutex.
func dumpInfoIdentity(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent := abs
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", err
		}
		suffix = append(suffix, filepath.Base(parent))
		parent = next
	}
}

// SameFile, rather than case folding, recognizes actual filesystem aliases.
// All registered paths have an existing base, including registrations made
// before their hash directory is built. Distinct case-sensitive bases stay apart.
func sameDumpInfoCachePath(a, b string) bool {
	ai, ae := os.Stat(a)
	bi, be := os.Stat(b)
	if ae == nil && be == nil {
		return os.SameFile(ai, bi)
	}
	if filepath.Base(a) != filepath.Base(b) {
		return false
	}
	ai, ae = os.Stat(filepath.Dir(a))
	bi, be = os.Stat(filepath.Dir(b))
	return ae == nil && be == nil && os.SameFile(ai, bi)
}

func sameDumpInfoOwner(a, b string) bool {
	if a == b {
		return true
	}
	ai, ae := os.Stat(a)
	bi, be := os.Stat(b)
	return ae == nil && be == nil && os.SameFile(ai, bi)
}

func openDumpInfoMetadata(cpath, dumpDir string) (*DumpInfoMetadata, error) {
	expected, err := filepath.Abs(dumpDir)
	if err != nil {
		return nil, err
	}
	// Establish missing bases before matching them. This creates no hash directory
	// or mapping; it avoids speculative case folding when BOTH bases are absent.
	if err := os.MkdirAll(filepath.Dir(cpath), 0o755); err != nil {
		return nil, err
	}
	key, err := dumpInfoIdentity(cpath)
	if err != nil {
		return nil, err
	}
	dumpInfoRegistry.Lock()
	state := dumpInfoRegistry.paths[key]
	if state == nil {
		for _, candidate := range dumpInfoRegistry.paths {
			if sameDumpInfoCachePath(key, candidate.path) {
				state = candidate
				break
			}
		}
	}
	if state != nil && !sameDumpInfoOwner(expected, state.dumpPath) {
		dumpInfoRegistry.Unlock()
		return nil, errors.New("dump: metadata cache is already registered for a different dump")
	}
	if state == nil {
		state = &dumpInfoState{path: key, dumpPath: expected}
		dumpInfoRegistry.paths[key] = state
	}
	state.refs++
	dumpInfoRegistry.Unlock()
	state.mu.Lock()
	// Import only valid bounded metadata. Retained fields take precedence over
	// disk fields, so deletion or an unrelated writer cannot erase known extras.
	if fields, err := readDumpInfoFields(state.path, state.dumpPath); err == nil {
		if merged, err := mergeDumpInfoExtras(extraDumpInfoFields(fields), state.extras); err == nil {
			state.extras = merged
		}
	}
	state.mu.Unlock()
	return &DumpInfoMetadata{state: state}, nil
}

// Close releases retention and is safe to repeat or race with Update/Remove.
func (m *DumpInfoMetadata) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	dumpInfoRegistry.Lock()
	m.state.refs--
	if m.state.refs == 0 {
		delete(dumpInfoRegistry.paths, m.state.path)
	}
	dumpInfoRegistry.Unlock()
	return nil
}

// Update upserts fields in input order, retaining the position of existing
// fields. It rejects duplicates, reserved names, invalid JSON, >64 fields or
// >64 KiB of supplemental JSON. Errors leave both prior bytes and retained
// values unchanged. Scans/computation belong outside this call and its locks.
func (m *DumpInfoMetadata) Update(fields ...DumpInfoField) error {
	copied, err := validateDumpInfoExtras(fields)
	if err != nil {
		return err
	}
	return m.changeExtras(copied, nil)
}

// Remove deletes named additional members from both the CURRENT document and
// retained state. Missing extras are harmless; standard names and invalid or
// duplicate names are rejected by the same rules as Update. It never creates
// an extra-only document. A failed write preserves prior bytes and values.
func (m *DumpInfoMetadata) Remove(names ...string) error {
	if len(names) > maxDumpInfoExtras {
		return errors.New("dump: too many supplemental fields")
	}
	fields := make([]DumpInfoField, len(names))
	for i, name := range names {
		fields[i] = DumpInfoField{name, json.RawMessage("null")}
	}
	validated, err := validateDumpInfoExtras(fields)
	if err != nil {
		return err
	}
	return m.changeExtras(nil, validated)
}

func (m *DumpInfoMetadata) changeExtras(updates, removed []DumpInfoField) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return errors.New("dump: metadata handle is closed")
	}
	state := m.state
	state.mu.Lock()
	defer state.mu.Unlock()
	current, err := readDumpInfoFields(state.path, state.dumpPath)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDumpInfoUnavailable, err)
	}
	extras, err := mergeDumpInfoExtras(extraDumpInfoFields(current), state.extras)
	if err != nil {
		return err
	}
	if len(removed) != 0 {
		names := make(map[string]bool, len(removed))
		for _, field := range removed {
			names[field.Name] = true
		}
		kept := make([]DumpInfoField, 0, len(extras))
		for _, field := range extras {
			if !names[field.Name] {
				kept = append(kept, field)
			}
		}
		extras = kept
	}
	extras, err = mergeDumpInfoExtras(extras, updates)
	if err != nil {
		return err
	}
	standard := make([]DumpInfoField, 0, 7)
	for _, field := range current {
		if reservedDumpInfoName(field.Name) {
			standard = append(standard, field)
		}
	}
	data, err := encodeDumpInfoFields(append(standard, extras...))
	if err != nil {
		return err
	}
	if err := replaceDumpInfo(state.path, filepath.Join(state.path, "dump.json"), data); err != nil {
		return err
	}
	state.extras = extras
	return nil
}

func reservedDumpInfoName(name string) bool {
	switch name {
	case "schema", "dump_path", "modules", "build_seconds", "built_at", "mcp_1c_version", "platform":
		return true
	}
	return false
}

func validateDumpInfoExtras(fields []DumpInfoField) ([]DumpInfoField, error) {
	if len(fields) > maxDumpInfoExtras {
		return nil, errors.New("dump: too many supplemental fields")
	}
	copied := make([]DumpInfoField, 0, len(fields))
	seen := make(map[string]bool)
	size := 2
	for _, field := range fields {
		if field.Name == "" || !utf8.ValidString(field.Name) || len(field.Name) > 256 || reservedDumpInfoName(field.Name) || seen[field.Name] {
			return nil, fmt.Errorf("dump: invalid, duplicate or reserved supplemental name %q", field.Name)
		}
		seen[field.Name] = true
		encodedName, _ := json.Marshal(field.Name)
		size += len(encodedName) + len(field.Value) + 2
		if size > maxDumpInfoExtrasBytes {
			return nil, errors.New("dump: supplemental fields exceed 64 KiB")
		}
		if !json.Valid(field.Value) {
			return nil, fmt.Errorf("dump: invalid JSON for %q", field.Name)
		}
		copied = append(copied, DumpInfoField{field.Name, append(json.RawMessage(nil), field.Value...)})
	}
	return copied, nil
}

func mergeDumpInfoExtras(base, updates []DumpInfoField) ([]DumpInfoField, error) {
	merged := append([]DumpInfoField(nil), base...)
	for _, field := range updates {
		found := false
		for i := range merged {
			if merged[i].Name == field.Name {
				merged[i] = field
				found = true
				break
			}
		}
		if !found {
			merged = append(merged, field)
		}
	}
	return validateDumpInfoExtras(merged)
}

func extraDumpInfoFields(fields []DumpInfoField) []DumpInfoField {
	var extras []DumpInfoField
	for _, field := range fields {
		if !reservedDumpInfoName(field.Name) {
			extras = append(extras, field)
		}
	}
	return extras
}

func readDumpInfoFields(cpath, expectedDump string) ([]DumpInfoField, error) {
	path := filepath.Join(cpath, "dump.json")
	// Reject non-regular mappings before Open (opening a FIFO could block).
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxDumpInfoBytes {
		return nil, errors.New("dump: mapping is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxDumpInfoBytes {
		return nil, errors.New("dump: mapping is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxDumpInfoBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDumpInfoBytes {
		return nil, errors.New("dump: mapping exceeds 1 MiB")
	}
	fields, err := decodeDumpInfoFields(data)
	if err != nil {
		return nil, err
	}
	var standard dumpInfo
	if err := json.Unmarshal(data, &standard); err != nil {
		return nil, err
	}
	required := map[string]bool{"schema": false, "dump_path": false, "modules": false, "build_seconds": false, "built_at": false, "platform": false}
	for _, field := range fields {
		if _, ok := required[field.Name]; ok {
			required[field.Name] = string(field.Value) != "null"
		}
	}
	for name, present := range required {
		if !present {
			return nil, fmt.Errorf("dump: missing standard field %s", name)
		}
	}
	if standard.Schema != 1 || standard.DumpPath == "" || standard.Modules < 0 || standard.BuildSeconds < 0 || standard.Platform == "" {
		return nil, errors.New("dump: invalid standard mapping")
	}
	if !filepath.IsAbs(standard.DumpPath) || !sameDumpInfoOwner(filepath.Clean(standard.DumpPath), expectedDump) {
		return nil, errors.New("dump: mapping belongs to a different dump")
	}
	if _, err := time.Parse(time.RFC3339, standard.BuiltAt); err != nil {
		return nil, err
	}
	if _, err := validateDumpInfoExtras(extraDumpInfoFields(fields)); err != nil {
		return nil, err
	}
	return fields, nil
}

func decodeDumpInfoFields(data []byte) ([]DumpInfoField, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("dump: mapping must be an object")
	}
	var fields []DumpInfoField
	seen := make(map[string]bool)
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok || seen[name] {
			return nil, errors.New("dump: duplicate or invalid mapping member")
		}
		seen[name] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		fields = append(fields, DumpInfoField{name, value})
		if len(fields) > maxDumpInfoExtras+7 {
			return nil, errors.New("dump: too many mapping fields")
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("dump: trailing mapping data")
	}
	return fields, nil
}

func encodeDumpInfoFields(fields []DumpInfoField) ([]byte, error) {
	var compact bytes.Buffer
	compact.WriteByte('{')
	for i, field := range fields {
		if i > 0 {
			compact.WriteByte(',')
		}
		name, err := json.Marshal(field.Name)
		if err != nil {
			return nil, err
		}
		compact.Write(name)
		compact.WriteByte(':')
		compact.Write(field.Value)
	}
	compact.WriteByte('}')
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, compact.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	if formatted.Len() > maxDumpInfoBytes {
		return nil, errors.New("dump: encoded mapping exceeds 1 MiB")
	}
	return formatted.Bytes(), nil
}

func publishDumpInfo(cpath string, standard dumpInfo) error {
	m, err := openDumpInfoMetadata(cpath, standard.DumpPath)
	if err != nil {
		return err
	}
	defer m.Close()
	state := m.state
	state.mu.Lock()
	defer state.mu.Unlock()
	data, err := json.Marshal(standard)
	if err != nil {
		return err
	}
	fields, err := decodeDumpInfoFields(data)
	if err != nil {
		return err
	}
	data, err = encodeDumpInfoFields(append(fields, state.extras...))
	if err != nil {
		return err
	}
	return replaceDumpInfo(state.path, filepath.Join(state.path, "dump.json"), data)
}
