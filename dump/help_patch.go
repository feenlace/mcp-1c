package dump

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"html"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

type HelpPatchRequest struct {
	ManifestPath         string `json:"manifest_path"`
	ExpectedSourceSHA256 string `json:"expected_source_sha256"`
	Language             string `json:"language"`
	Content              string `json:"content,omitempty"`
	HTML                 string `json:"html,omitempty"`
	Overwrite            bool   `json:"overwrite,omitempty"`
}
type HelpPathChange struct {
	Path           string `json:"path"`
	OriginalSHA256 string `json:"original_sha256,omitempty"`
	SHA256         string `json:"sha256"`
	BackupPath     string `json:"backup_path,omitempty"`
}
type HelpPatchResult struct {
	Status   string           `json:"status"`
	Source   ExternalSource   `json:"source"`
	Changes  []HelpPathChange `json:"changes"`
	Warnings []string         `json:"warnings,omitempty"`
}

// This descriptor is the native-emitted2.20 two-file profile. The root manifest is never rewritten.
const nativeHelpDescriptor = "\xef\xbb\xbf<?xml version=\"1.0\" encoding=\"UTF-8\"?>\r\n<Help xmlns=\"http://v8.1c.ru/8.3/xcf/extrnprops\" xmlns:xs=\"http://www.w3.org/2001/XMLSchema\" xmlns:xsi=\"http://www.w3.org/2001/XMLSchema-instance\" version=\"2.20\">\r\n\t<Page>ru</Page>\r\n</Help>"

func helpHTML(input HelpPatchRequest, name string) ([]byte, error) {
	if !utf8.ValidString(input.Content) || !utf8.ValidString(input.HTML) || strings.IndexFunc(input.Content, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' }) >= 0 || len(input.Content)+len(input.HTML) > 256<<10 || (input.Content == "") == (input.HTML == "") {
		return nil, errors.New("supply either nonempty UTF-8 content or a safe HTML fragment, up to256KiB")
	}
	body := "<pre>" + html.EscapeString(input.Content) + "</pre>"
	if input.HTML != "" {
		allowed := map[string]bool{"p": true, "br": true, "h1": true, "h2": true, "h3": true, "b": true, "strong": true, "i": true, "em": true, "pre": true, "code": true, "ul": true, "ol": true, "li": true, "blockquote": true, "table": true, "tr": true, "td": true, "th": true, "thead": true, "tbody": true}
		d := xml.NewDecoder(strings.NewReader("<fragment>" + input.HTML + "</fragment>"))
		depth, nodes := 0, 0
		for {
			t, err := d.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, errors.New("HTML must be a well-formed safe fragment; use content for plain text")
			}
			switch t := t.(type) {
			case xml.StartElement:
				depth++
				nodes++
				if depth > 32 || nodes > 5000 || (depth == 1 && (t.Name.Local != "fragment" || t.Name.Space != "")) || (depth != 1 && (!allowed[t.Name.Local] || t.Name.Space != "")) || len(t.Attr) != 0 {
					return nil, errors.New("HTML permits only documented formatting tags, without attributes, scripts or resources")
				}
			case xml.EndElement:
				depth--
			case xml.Directive, xml.ProcInst, xml.Comment:
				return nil, errors.New("HTML directives and processing instructions are unsupported")
			}
		}
		body = input.HTML
	}
	return []byte("<!DOCTYPE html><html><head><meta charset=\"utf-8\"></meta><title>" + html.EscapeString(name) + "</title></head><body>" + body + "</body></html>"), nil
}

func helpDescriptor(data []byte) ([]byte, error) {
	d := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	depth, roots, pages := 0, 0, 0
	page := ""
	inside := false
	end := int64(0)
	for {
		before := d.InputOffset()
		t, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("malformed existing help descriptor")
		}
		switch t := t.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				roots++
				version := ""
				defaultNamespace := ""
				for _, a := range t.Attr {
					if a.Name.Local == "version" {
						version = a.Value
					}
					if a.Name.Space == "" && a.Name.Local == "xmlns" {
						defaultNamespace = a.Value
					}
				}
				if roots != 1 || t.Name.Local != "Help" || t.Name.Space != "http://v8.1c.ru/8.3/xcf/extrnprops" || defaultNamespace != t.Name.Space || version != "2.20" {
					return nil, errors.New("unsupported help descriptor profile")
				}
			}
			if depth == 2 && t.Name.Local == "Page" && t.Name.Space == "http://v8.1c.ru/8.3/xcf/extrnprops" {
				inside = true
				page = ""
			}
		case xml.CharData:
			if inside {
				page += string(t)
			} else if depth == 0 && strings.Trim(string(t), " \t\r\n") != "" {
				return nil, errors.New("help descriptor has text outside its root")
			}
		case xml.EndElement:
			if depth == 2 && inside {
				if page == "ru" {
					pages++
				}
				inside = false
			}
			if depth == 1 {
				if before == d.InputOffset() {
					return nil, errors.New("self-closing help descriptor root is unsupported; use the native explicit-closing profile")
				}
				end = before
			}
			depth--
		case xml.Directive:
			return nil, errors.New("help descriptor directives are unsupported")
		case xml.ProcInst:
			if t.Target != "xml" || roots != 0 {
				return nil, errors.New("help descriptor processing instructions are unsupported")
			}
		}
	}
	if roots != 1 || pages > 1 || end == 0 {
		return nil, errors.New("ambiguous help descriptor")
	}
	if pages == 1 {
		return data, nil
	}
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		end += 3
	}
	result := append([]byte(nil), data[:end]...)
	result = append(result, []byte("\t<Page>ru</Page>\r\n")...)
	result = append(result, data[end:]...)
	if err := externalXMLDocument(result); err != nil {
		return nil, err
	}
	var check struct {
		Pages []string `xml:"http://v8.1c.ru/8.3/xcf/extrnprops Page"`
	}
	if err := xml.Unmarshal(bytes.TrimPrefix(result, []byte{0xef, 0xbb, 0xbf}), &check); err != nil {
		return nil, errors.New("malformed updated help descriptor")
	}
	count := 0
	for _, page := range check.Pages {
		if page == "ru" {
			count++
		}
	}
	if count != 1 {
		return nil, errors.New("updated help descriptor requires one ru Page in the native namespace")
	}
	return result, nil
}

func helpToken() string { var b [12]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func helpMkdir(root *os.Root, path string, created *[]string) error {
	parts := strings.Split(path, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		st, err := externalStat(root, p, true)
		if err != nil {
			return err
		}
		if st == nil {
			if err := root.Mkdir(p, 0700); err != nil {
				return errors.New("cannot create selected help directory")
			}
			*created = append(*created, p)
		} else if !st.IsDir() {
			return errors.New("help parent is not a directory")
		}
	}
	return nil
}
func helpWrite(root *os.Root, path string, data []byte) error {
	f, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("help staging path collision")
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("help staging write failed")
	}
	return nil
}

// PatchHelp changes only the native-proven descriptor and ru.html. Unknown main XML bytes are preserved.
func PatchHelp(ctx context.Context, projectRoot string, input HelpPatchRequest) (HelpPatchResult, error) {
	result := HelpPatchResult{Status: "failed", Changes: []HelpPathChange{}}
	if input.Language != "ru" {
		return result, errors.New("this native-proven help profile supports language ru")
	}
	source, err := InspectExternalSource(ctx, projectRoot, input.ManifestPath)
	if err != nil {
		return result, err
	}
	result.Source = source
	if input.ExpectedSourceSHA256 == "" || source.SourceTreeSHA256 != input.ExpectedSourceSHA256 {
		return result, errors.New("selected source digest mismatch; inspect the exact source set before patching")
	}
	page, err := helpHTML(input, source.Name)
	if err != nil {
		return result, err
	}
	root, err := openExternalRoot(projectRoot)
	if err != nil {
		return result, err
	}
	defer root.Close()
	objectDir := filepath.ToSlash(filepath.Join(filepath.Dir(input.ManifestPath), source.Name))
	paths := []string{objectDir + "/Ext/Help.xml", objectDir + "/Ext/Help/ru.html"}
	contents := [][]byte{[]byte(nativeHelpDescriptor), page}
	originals := make([][]byte, 2)
	exists := make([]bool, 2)
	for i, path := range paths {
		// Parent directories may be absent in native no-help source. The full walk above already refused existing symlinks.
		if _, e := root.Lstat(path); e == nil {
			b, e := externalRead(ctx, root, path)
			if e != nil {
				return result, e
			}
			originals[i] = b
			exists[i] = true
		} else if !errors.Is(e, os.ErrNotExist) {
			return result, errors.New("help path is unavailable")
		}
	}
	if (exists[0] || exists[1]) && !input.Overwrite {
		return result, errors.New("help already exists; inspect the source and explicitly select overwrite")
	}
	if exists[0] {
		contents[0], err = helpDescriptor(originals[0])
		if err != nil {
			return result, err
		}
	}
	if bytes.Equal(contents[0], originals[0]) && bytes.Equal(contents[1], originals[1]) {
		result.Status = "unchanged"
		return result, nil
	}
	lockPath := input.ManifestPath + ".help.lock"
	lock, err := root.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return result, errors.New("selected external object is locked; retry after its writer finishes")
	}
	defer lock.Close()
	lockInfo, _ := lock.Stat()
	defer func() {
		if st, e := root.Lstat(lockPath); e == nil && lockInfo != nil && os.SameFile(lockInfo, st) {
			root.Remove(lockPath)
		}
	}()
	current, err := InspectExternalSource(ctx, projectRoot, input.ManifestPath)
	if err != nil || current.SourceTreeSHA256 != source.SourceTreeSHA256 {
		return result, errors.New("external source changed before patch; no changes applied")
	}
	var created []string
	defer func() {
		for i := len(created) - 1; i >= 0; i-- {
			root.Remove(created[i])
		}
	}()
	if err := helpMkdir(root, objectDir+"/Ext/Help", &created); err != nil {
		return result, err
	}
	backupDir := filepath.ToSlash(filepath.Join(filepath.Dir(input.ManifestPath), ".mcp-help-backup-"+helpToken()))
	if err := root.Mkdir(backupDir, 0700); err != nil {
		return result, errors.New("cannot prepare help backup directory")
	}
	keepBackup := false
	defer func() {
		if !keepBackup {
			root.RemoveAll(backupDir)
		}
	}()
	for i := range paths {
		if err := helpWrite(root, backupDir+"/stage-"+string(rune('0'+i)), contents[i]); err != nil {
			return result, err
		}
	}
	moved := make([]bool, 2)
	published := make([]bool, 2)
	rollback := func() bool {
		ok := true
		for i := 1; i >= 0; i-- {
			if published[i] {
				b, e := externalRead(context.WithoutCancel(ctx), root, paths[i])
				if e != nil || !bytes.Equal(b, contents[i]) {
					ok = false
					continue
				}
				if root.Remove(paths[i]) != nil {
					ok = false
					continue
				}
			}
			if moved[i] {
				if root.Link(backupDir+"/original-"+string(rune('0'+i)), paths[i]) != nil {
					ok = false
				}
			}
		}
		return ok
	}
	fail := func() (HelpPatchResult, error) {
		if rollback() {
			result.Changes = nil
			return result, errors.New("help patch failed; original help restored")
		}
		keepBackup = true
		result.Status = "partial"
		result.Warnings = []string{"independent file changes prevented automatic recovery; inspect returned backups before restoring"}
		return result, errors.New("help patch requires explicit conflict recovery")
	}
	for i, path := range paths {
		change := HelpPathChange{Path: path, SHA256: externalHash(contents[i])}
		if exists[i] {
			change.OriginalSHA256 = externalHash(originals[i])
			change.BackupPath = backupDir + "/original-" + string(rune('0'+i))
		}
		result.Changes = append(result.Changes, change)
		if err := ctx.Err(); err != nil {
			return fail()
		}
		if exists[i] {
			b, e := externalRead(ctx, root, path)
			if e != nil || !bytes.Equal(b, originals[i]) {
				return fail()
			}
			if root.Rename(path, change.BackupPath) != nil {
				return fail()
			}
			moved[i] = true
			b, e = externalRead(ctx, root, change.BackupPath)
			if e != nil || !bytes.Equal(b, originals[i]) {
				return fail()
			}
		}
		if root.Link(backupDir+"/stage-"+string(rune('0'+i)), path) != nil {
			return fail()
		}
		published[i] = true
	}
	updated, err := InspectExternalSource(ctx, projectRoot, input.ManifestPath)
	if err != nil {
		return fail()
	}
	// Only the admitted two files may change, including during the commit window.
	for _, file := range source.Files {
		if file.Path == paths[0] || file.Path == paths[1] {
			continue
		}
		found := false
		for _, after := range updated.Files {
			if after.Path == file.Path && after.SHA256 == file.SHA256 {
				found = true
				break
			}
		}
		if !found {
			return fail()
		}
	}
	for _, file := range updated.Files {
		if file.Path == paths[0] || file.Path == paths[1] {
			continue
		}
		found := false
		for _, before := range source.Files {
			if before.Path == file.Path && before.SHA256 == file.SHA256 {
				found = true
				break
			}
		}
		if !found {
			return fail()
		}
	}
	result.Source = updated
	result.Status = "patched"
	for i := range moved {
		if moved[i] {
			keepBackup = true
		}
		root.Remove(backupDir + "/stage-" + string(rune('0'+i)))
	}
	return result, nil
}
