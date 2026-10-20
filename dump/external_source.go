package dump

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const externalMaxFile = 8 << 20
const externalMaxTotal = 32 << 20

type ExternalSourceFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type ExternalSource struct {
	ManifestPath     string               `json:"manifest_path"`
	Kind             string               `json:"kind"`
	Name             string               `json:"name"`
	FormatVersion    string               `json:"format_version"`
	Files            []ExternalSourceFile `json:"files"`
	SourceTreeSHA256 string               `json:"source_tree_sha256"`
}

func externalPath(p string) bool {
	return len(p) <= 1024 && utf8.ValidString(p) && strings.IndexFunc(p, unicode.IsControl) < 0 && p != "." && filepath.IsLocal(p) && filepath.ToSlash(filepath.Clean(p)) == p && !strings.Contains(p, "\\")
}
func externalHash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func externalStat(root *os.Root, path string, missing bool) (os.FileInfo, error) {
	if !externalPath(path) {
		return nil, errors.New("select a clean relative external-object source path")
	}
	parts := strings.Split(path, "/")
	for i := range parts {
		st, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if missing && errors.Is(err, os.ErrNotExist) && i == len(parts)-1 {
			return nil, nil
		}
		if err != nil {
			return nil, errors.New("external source path is unavailable")
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("external source symlinks are unsupported")
		}
		if i == len(parts)-1 {
			return st, nil
		}
		if !st.IsDir() {
			return nil, errors.New("external source parent is not a directory")
		}
	}
	return nil, errors.New("invalid external source path")
}
func externalRead(ctx context.Context, root *os.Root, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	st, err := externalStat(root, path, false)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > externalMaxFile {
		return nil, errors.New("external source requires regular files up to8MiB")
	}
	f, err := root.OpenFile(path, os.O_RDONLY|nonblockOpenFlag, 0)
	if err != nil {
		return nil, errors.New("external source cannot be opened")
	}
	defer f.Close()
	current, err := f.Stat()
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(st, current) {
		return nil, errors.New("external source changed during open")
	}
	data, err := io.ReadAll(io.LimitReader(f, externalMaxFile+1))
	if err != nil || len(data) > externalMaxFile {
		return nil, errors.New("external source read limit exceeded")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}
func openExternalRoot(path string) (*os.Root, error) {
	if path == "" {
		return nil, errors.New("select an existing project source directory")
	}
	st, err := os.Lstat(path)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("external source root must be a directory, not a symlink")
	}
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, errors.New("external source root cannot be opened")
	}
	return r, nil
}

// InspectExternalSource admits the native-proven hierarchical2.20 external object profile.
// The digest is SHA256 of sorted UTF-8 relative-path + NUL + file SHA256 + LF records.
// It binds the root XML and every regular file below the selected object's directory, not unrelated projects.
func InspectExternalSource(ctx context.Context, projectRoot, manifest string) (ExternalSource, error) {
	var out ExternalSource
	root, err := openExternalRoot(projectRoot)
	if err != nil {
		return out, err
	}
	defer root.Close()
	data, err := externalRead(ctx, root, manifest)
	if err != nil {
		return out, err
	}
	out.ManifestPath = manifest
	d := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	depth, roots, objects, properties, names, nodes := 0, 0, 0, 0, 0, 0
	inProperties := false
	nameDepth := 0
	for {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		t, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, errors.New("malformed external manifest XML")
		}
		switch t := t.(type) {
		case xml.StartElement:
			depth++
			nodes++
			if depth > 64 || nodes > 50000 {
				return out, errors.New("external manifest XML limit exceeded")
			}
			if depth == 1 {
				roots++
				if roots != 1 || t.Name.Local != "MetaDataObject" || t.Name.Space != "http://v8.1c.ru/8.3/MDClasses" {
					return out, errors.New("unsupported external manifest root")
				}
				for _, a := range t.Attr {
					if a.Name.Local == "version" {
						out.FormatVersion = a.Value
					}
				}
			} else if depth == 2 {
				objects++
				if objects != 1 || (t.Name.Local != "ExternalReport" && t.Name.Local != "ExternalDataProcessor") || t.Name.Space != "http://v8.1c.ru/8.3/MDClasses" {
					return out, errors.New("select one ExternalReport or ExternalDataProcessor")
				}
				out.Kind = t.Name.Local
			} else if depth == 3 && t.Name.Local == "Properties" {
				properties++
				inProperties = true
			} else if depth == 4 && inProperties && t.Name.Local == "Name" {
				names++
				nameDepth = depth
			}
		case xml.EndElement:
			if depth == nameDepth {
				nameDepth = 0
			}
			if depth == 3 {
				inProperties = false
			}
			depth--
		case xml.CharData:
			if nameDepth != 0 {
				out.Name += string(t)
			} else if depth == 0 && strings.TrimSpace(string(t)) != "" {
				return out, errors.New("external manifest has text outside its root")
			}
		case xml.Directive:
			return out, errors.New("external manifest directives are unsupported")
		case xml.ProcInst:
			if t.Target != "xml" || roots != 0 {
				return out, errors.New("external manifest processing instructions are unsupported")
			}
		}
	}
	if roots != 1 || objects != 1 || properties != 1 || names != 1 || !summaryIdentifier(out.Name) || out.FormatVersion != "2.20" || filepath.Base(manifest) != out.Name+".xml" {
		return out, errors.New("unsupported or ambiguous external source profile; supported profile is hierarchical2.20 with root <Name>.xml")
	}
	out.Files = []ExternalSourceFile{{Path: manifest, SHA256: externalHash(data), Size: int64(len(data))}}
	total := len(data)
	dir := filepath.ToSlash(filepath.Join(filepath.Dir(manifest), out.Name))
	st, err := externalStat(root, dir, true)
	if err != nil {
		return out, err
	}
	if st != nil {
		if !st.IsDir() {
			return out, errors.New("external object source path must be a directory")
		}
		err = fs.WalkDir(root.FS(), dir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return errors.New("external source traversal failed")
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if strings.Count(path, "/") > 32 || entry.Type()&os.ModeSymlink != 0 {
				return errors.New("external source symlink or depth limit")
			}
			if entry.IsDir() {
				return nil
			}
			if len(out.Files) >= 1024 {
				return errors.New("external source file-count limit exceeded")
			}
			b, err := externalRead(ctx, root, path)
			if err != nil {
				return err
			}
			total += len(b)
			if total > externalMaxTotal {
				return errors.New("external source exceeds32MiB")
			}
			if strings.EqualFold(filepath.Ext(path), ".xml") {
				decoder := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})))
				for {
					t, e := decoder.Token()
					if e == io.EOF {
						break
					}
					if e != nil {
						return errors.New("malformed referenced source XML")
					}
					if _, ok := t.(xml.Directive); ok {
						return errors.New("referenced source XML directives are unsupported")
					}
				}
			}
			out.Files = append(out.Files, ExternalSourceFile{Path: path, SHA256: externalHash(b), Size: int64(len(b))})
			return nil
		})
		if err != nil {
			return out, err
		}
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	var record strings.Builder
	for _, f := range out.Files {
		record.WriteString(f.Path)
		record.WriteByte(0)
		record.WriteString(f.SHA256)
		record.WriteByte('\n')
	}
	out.SourceTreeSHA256 = externalHash([]byte(record.String()))
	return out, nil
}
