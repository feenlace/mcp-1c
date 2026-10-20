package tools

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const schemaRegistrationFormat = "mcp-1c-schema-registration-v1"
const schemaMaxBytes = 4 << 20
const schemaManifestMaxBytes = 1 << 20

type SchemaRegistrationRequest struct {
	XSDPath        string `json:"xsd_path"`
	Version        string `json:"version"`
	SourceURL      string `json:"source_url,omitempty"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
	ManifestPath   string `json:"manifest_path"`
}

// SchemaRegistration records provenance supplied by the caller, not authoritative validation.
type SchemaRegistration struct {
	XSDPath   string `json:"xsd_path"`
	Version   string `json:"version"`
	SourceURL string `json:"source_url,omitempty"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

type SchemaRegistrationResult struct {
	Schema         SchemaRegistration `json:"schema"`
	ManifestPath   string             `json:"manifest_path"`
	ManifestSHA256 string             `json:"manifest_sha256"`
	Status         string             `json:"status"`
	BackupPath     string             `json:"backup_path,omitempty"`
}

type schemaManifest struct {
	Format  string               `json:"format"`
	Schemas []SchemaRegistration `json:"schemas"`
}

func schemaText(s string, limit int) bool {
	return utf8.ValidString(s) && len(s) <= limit && strings.IndexFunc(s, unicode.IsControl) < 0
}

func schemaHash(s string) bool {
	_, err := hex.DecodeString(s)
	return len(s) == 64 && err == nil
}

func recognizeXSD(ctx context.Context, data []byte) error {
	d := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	depth, roots, nodes := 0, 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		t, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("malformed XSD XML")
		}
		switch t := t.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots != 1 || t.Name.Local != "schema" || t.Name.Space != "http://www.w3.org/2001/XMLSchema" {
					return errors.New("XSD must have one XML Schema namespace schema root")
				}
			}
			depth++
			nodes++
			if depth > 64 || nodes > 50000 {
				return errors.New("XSD depth or element limit exceeded")
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(t)) != "" {
				return errors.New("XSD has text outside its root")
			}
		case xml.Directive:
			return errors.New("XSD directives and entities are unsupported")
		case xml.ProcInst:
			if t.Target != "xml" || roots != 0 {
				return errors.New("XSD processing instructions are unsupported")
			}
		}
	}
	if roots != 1 || depth != 0 {
		return errors.New("XSD root is missing or incomplete")
	}
	return nil
}

// RegisterSchema reads a supplied local XSD and updates only its selected provenance manifest.
// URLs are opaque declarations: no network, imports or schema validation are performed.
func RegisterSchema(ctx context.Context, projectRoot string, input SchemaRegistrationRequest) (SchemaRegistrationResult, error) {
	var result SchemaRegistrationResult
	if !localPath(input.XSDPath) || !localPath(input.ManifestPath) || input.XSDPath == input.ManifestPath {
		return result, errors.New("select distinct relative XSD and manifest paths")
	}
	if strings.TrimSpace(input.Version) == "" || !schemaText(input.Version, 256) || !schemaText(input.SourceURL, 2048) {
		return result, errors.New("supply a nonempty schema version and bounded provenance text")
	}
	if input.ExpectedSHA256 != "" && !schemaHash(input.ExpectedSHA256) {
		return result, errors.New("expected_sha256 must be a SHA-256 hex digest")
	}
	root, err := openLocalRoot(projectRoot)
	if err != nil {
		return result, err
	}
	defer root.Close()
	data, err := localRead(ctx, root, input.XSDPath, schemaMaxBytes)
	if err != nil {
		return result, err
	}
	if err := recognizeXSD(ctx, data); err != nil {
		return result, err
	}
	hash := localHash(data)
	if input.ExpectedSHA256 != "" && !strings.EqualFold(input.ExpectedSHA256, hash) {
		return result, errors.New("XSD hash mismatch; reread the selected file before registering")
	}
	st, err := localStat(root, input.ManifestPath, true)
	if err != nil {
		return result, err
	}
	if st != nil && !st.Mode().IsRegular() {
		return result, errors.New("registration manifest must be a regular file")
	}
	// Serialize only this manifest. A stale lock is an explicit recovery case, never silently removed.
	lockPath := input.ManifestPath + ".lock"
	lock, err := root.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return result, errors.New("manifest is locked or its lock path collides; retry after its current writer finishes, or inspect a stale lock before removing it")
	}
	lockInfo, _ := lock.Stat()
	defer func() {
		lock.Close()
		if current, e := root.Lstat(lockPath); e == nil && lockInfo != nil && os.SameFile(lockInfo, current) {
			root.Remove(lockPath)
		}
	}()
	manifest := schemaManifest{Format: schemaRegistrationFormat, Schemas: []SchemaRegistration{}}
	var original []byte
	// Reread after acquiring the lock, so concurrent calls cannot overwrite a completed registration.
	st, err = localStat(root, input.ManifestPath, true)
	if err != nil {
		return result, err
	}
	if st != nil {
		original, err = localRead(ctx, root, input.ManifestPath, schemaManifestMaxBytes)
		if err != nil {
			return result, err
		}
		if err := localDecode(original, &manifest); err != nil || manifest.Format != schemaRegistrationFormat || manifest.Schemas == nil || len(manifest.Schemas) > 512 {
			return result, errors.New("existing schema manifest is malformed or unsupported; preserve it and select a new manifest path")
		}
	}
	seen := map[string]bool{}
	index := -1
	for i, entry := range manifest.Schemas {
		if !localPath(entry.XSDPath) || seen[entry.XSDPath] || strings.TrimSpace(entry.Version) == "" || !schemaText(entry.Version, 256) || !schemaText(entry.SourceURL, 2048) || !schemaHash(entry.SHA256) || entry.Size < 0 || entry.Size > schemaMaxBytes {
			return result, errors.New("existing schema manifest has invalid or conflicting records; preserve it and resolve the conflict")
		}
		seen[entry.XSDPath] = true
		if entry.XSDPath == input.XSDPath {
			index = i
		}
	}
	entry := SchemaRegistration{XSDPath: input.XSDPath, Version: input.Version, SourceURL: input.SourceURL, SHA256: hash, Size: int64(len(data))}
	result = SchemaRegistrationResult{Schema: entry, ManifestPath: input.ManifestPath, Status: "registered"}
	if index >= 0 {
		if manifest.Schemas[index] == entry {
			result.Status = "unchanged"
			result.ManifestSHA256 = localHash(original)
			return result, nil
		}
		manifest.Schemas[index] = entry
		result.Status = "updated"
	} else {
		if len(manifest.Schemas) >= 512 {
			return result, errors.New("schema manifest record limit reached")
		}
		manifest.Schemas = append(manifest.Schemas, entry)
	}
	sort.Slice(manifest.Schemas, func(i, j int) bool { return manifest.Schemas[i].XSDPath < manifest.Schemas[j].XSDPath })
	updated, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return result, err
	}
	updated = append(updated, '\n')
	if len(updated) > schemaManifestMaxBytes {
		return result, errors.New("schema manifest size limit reached")
	}
	currentXSD, err := localRead(ctx, root, input.XSDPath, schemaMaxBytes)
	if err != nil || !bytes.Equal(currentXSD, data) {
		return result, errors.New("XSD changed before registration; retry with its current hash")
	}
	if st == nil {
		if err := localCreate(ctx, root, input.ManifestPath, updated); err != nil {
			result.Status = "failed"
			return result, err
		}
	} else {
		backup, err := replaceSchemaManifest(ctx, root, input.ManifestPath, original, updated, st.Mode().Perm())
		result.BackupPath = backup
		if err != nil {
			result.Status = "failed"
			return result, err
		}
	}
	result.ManifestSHA256 = localHash(updated)
	return result, nil
}

func replaceSchemaManifest(ctx context.Context, root *os.Root, path string, original, updated []byte, mode os.FileMode) (string, error) {
	stage, err := localStage(ctx, root, path, updated)
	if err != nil {
		return "", err
	}
	defer root.Remove(stage)
	if err := root.Chmod(stage, mode); err != nil {
		return "", errors.New("cannot preserve manifest permissions")
	}
	current, err := localRead(ctx, root, path, schemaManifestMaxBytes)
	if err != nil || !bytes.Equal(current, original) {
		return "", errors.New("manifest changed before commit; no update applied")
	}
	backup := localUnique(path, ".bak")
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Move the original instead of blindly replacing it. Link below never overwrites a racing writer.
	if err := root.Rename(path, backup); err != nil {
		return "", errors.New("cannot preserve original manifest; no update applied")
	}
	moved, readErr := localRead(context.WithoutCancel(ctx), root, backup, schemaManifestMaxBytes)
	if readErr != nil || !bytes.Equal(moved, original) || ctx.Err() != nil {
		if root.Link(backup, path) == nil {
			root.Remove(backup)
			return "", errors.New("manifest changed or operation cancelled; original restored")
		}
		return backup, errors.New("manifest conflict; preserved backup requires inspection before recovery")
	}
	if err := root.Link(stage, path); err != nil {
		if root.Link(backup, path) == nil {
			root.Remove(backup)
			return "", errors.New("manifest publication failed; original restored")
		}
		return backup, errors.New("manifest publication conflict; original remains in the returned backup path, inspect before recovery")
	}
	return backup, nil
}

func SchemaRegistrationTool() *mcp.Tool {
	return &mcp.Tool{Name: "register_schema", Title: "Зарегистрировать локальную XSD", Description: "Записать SHA-256, размер и объявленные версию/источник локальной XSD в выбранный manifest проекта. Нужен --project-root (либо --dump); родительские каталоги должны существовать. source_url — только запись происхождения, без загрузки. Не валидирует XML и не подтверждает официальность схемы. Исходная XSD сохраняется; повтор без изменений — no-op, обновление manifest сохраняет backup.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"xsd_path":{"type":"string"},"version":{"type":"string"},"source_url":{"type":"string"},"expected_sha256":{"type":"string"},"manifest_path":{"type":"string"}},"required":["xsd_path","version","manifest_path"]}`)}
}

func NewSchemaRegistrationHandler(root string) mcp.ToolHandler {
	return WithToolErrors("Регистрация схемы не выполнена", func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input SchemaRegistrationRequest
		if err := localDecode(req.Params.Arguments, &input); err != nil {
			return nil, InvalidParams(argumentDecodeError(err))
		}
		result, err := RegisterSchema(ctx, root, input)
		if err != nil {
			if result.BackupPath != "" {
				return textError("Регистрация схемы не выполнена\n\nСохранённый backup manifest: " + result.BackupPath + ". Проверьте текущий manifest и backup перед восстановлением."), nil
			}
			return nil, err
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		return textResult(string(data)), nil
	})
}
