package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

func localDecode(data []byte, target any) error {
	if data = bytes.TrimSpace(data); len(data) == 0 || data[0] != '{' {
		return errors.New("expected one JSON object")
	}
	// Duplicate manifest keys could otherwise silently discard prior records.
	check := json.NewDecoder(bytes.NewReader(data))
	check.UseNumber()
	nodes := 0
	var value func(int) error
	value = func(depth int) error {
		nodes++
		if depth > 64 || nodes > 100000 {
			return errors.New("JSON depth or value limit exceeded")
		}
		token, err := check.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				seen := map[string]bool{}
				for check.More() {
					key, err := check.Token()
					if err != nil {
						return err
					}
					name, ok := key.(string)
					if !ok || seen[name] {
						return errors.New("duplicate or invalid JSON object key")
					}
					seen[name] = true
					if err := value(depth + 1); err != nil {
						return err
					}
				}
			case '[':
				for check.More() {
					if err := value(depth + 1); err != nil {
						return err
					}
				}
			default:
				return errors.New("unexpected JSON delimiter")
			}
			_, err = check.Token()
			return err
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}

func localHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func localPath(path string) bool {
	return len(path) <= 1024 && utf8.ValidString(path) && strings.IndexFunc(path, unicode.IsControl) < 0 && path != "." && filepath.IsLocal(path) && filepath.ToSlash(filepath.Clean(path)) == path && !strings.Contains(path, "\\")
}

func openLocalRoot(path string) (*os.Root, error) {
	if path == "" {
		return nil, errors.New("select an existing project directory with --project-root (or --dump)")
	}
	st, err := os.Lstat(path)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("project root must be an existing directory, not a symlink")
	}
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, errors.New("project root cannot be opened")
	}
	return r, nil
}

// localStat refuses symlinks even when they would remain inside the declared root.
func localStat(root *os.Root, path string, missingLeaf bool) (os.FileInfo, error) {
	if !localPath(path) {
		return nil, errors.New("use a clean relative project path without traversal")
	}
	parts := strings.Split(path, "/")
	for i := range parts {
		st, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if errors.Is(err, os.ErrNotExist) && missingLeaf && i == len(parts)-1 {
			return nil, nil
		}
		if err != nil {
			return nil, errors.New("selected project path is unavailable; its parent directory must exist")
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("project paths containing symlinks are unsupported")
		}
		if i < len(parts)-1 && !st.IsDir() {
			return nil, errors.New("project path parent must be a directory")
		}
		if i == len(parts)-1 {
			return st, nil
		}
	}
	return nil, errors.New("invalid project path")
}

func localRead(ctx context.Context, root *os.Root, path string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	selected, err := localStat(root, path, false)
	if err != nil {
		return nil, err
	}
	if !selected.Mode().IsRegular() || selected.Size() > limit {
		return nil, errors.New("selected file must be a bounded regular file")
	}
	f, err := root.OpenFile(path, os.O_RDONLY|localNonblock, 0)
	if err != nil {
		return nil, errors.New("selected file cannot be opened")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || !os.SameFile(selected, st) {
		return nil, errors.New("selected file changed during open")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("selected file read limit exceeded")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func localUnique(path, suffix string) string {
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		panic(err)
	}
	return filepath.ToSlash(filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"."+hex.EncodeToString(token[:])+suffix))
}

// Stage in the destination directory, then publish with Link: an existing output is never replaced.
func localStage(ctx context.Context, root *os.Root, path string, data []byte) (string, error) {
	if _, err := localStat(root, path, true); err != nil {
		return "", err
	}
	stage := localUnique(path, ".tmp")
	f, err := root.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", errors.New("cannot stage selected output")
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			root.Remove(stage)
		}
	}()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		return "", errors.New("output write failed")
	}
	if err := f.Sync(); err != nil {
		return "", errors.New("output sync failed")
	}
	if err := f.Close(); err != nil {
		return "", errors.New("output close failed")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ok = true
	return stage, nil
}

func localCreate(ctx context.Context, root *os.Root, path string, data []byte) error {
	st, err := localStat(root, path, true)
	if err != nil {
		return err
	}
	if st != nil {
		return errors.New("output already exists; select a separate new output path")
	}
	stage, err := localStage(ctx, root, path, data)
	if err != nil {
		return err
	}
	defer root.Remove(stage)
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := localStat(root, path, true); err != nil {
		return err
	}
	if err := root.Link(stage, path); err != nil {
		return errors.New("output collision or publication failure; original files preserved")
	}
	return nil
}
