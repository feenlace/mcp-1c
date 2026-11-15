package epf

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ConnectionFlags preserves unnamed single-base flags and accumulates named flags.
// Values contain file paths, never the code itself; String does not expose inputs.
type ConnectionFlags []string

func (v *ConnectionFlags) String() string     { return "" }
func (v *ConnectionFlags) Set(s string) error { *v = append(*v, s); return nil }

type Connection struct {
	Name, Listen, TokenFile, WebOrigin, Endpoint string
	Legacy                                       bool
}

func configurationError() error {
	return errors.New("epf_configuration_invalid: use one unnamed --epf-listen/--epf-token-file pair, or repeat both as NAME=VALUE with unique lowercase names, loopback ports and dedicated private files; name each optional --epf-web-origin too; do not mix named and unnamed flags")
}

func validName(s string) bool {
	if len(s) == 0 || len(s) > 64 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// Connections validates the entire configuration before any file is reserved.
func Connections(listens, files, origins ConnectionFlags) ([]Connection, error) {
	if len(listens)+len(files)+len(origins) == 0 {
		return nil, nil
	}
	if len(listens) == 0 {
		return nil, configurationError()
	}
	named := strings.Contains(listens[0], "=")
	parse := func(values ConnectionFlags) (map[string]string, error) {
		out := make(map[string]string)
		for _, v := range values {
			name, value := "default", v
			if named {
				var ok bool
				name, value, ok = strings.Cut(v, "=")
				if !ok || !validName(name) {
					return nil, configurationError()
				}
			}
			if value == "" {
				return nil, configurationError()
			}
			if _, exists := out[name]; exists {
				return nil, configurationError()
			}
			out[name] = value
		}
		return out, nil
	}
	lm, err := parse(listens)
	if err != nil {
		return nil, err
	}
	fm, err := parse(files)
	if err != nil {
		return nil, err
	}
	om, err := parse(origins)
	if err != nil {
		return nil, err
	}
	if len(lm) != len(fm) {
		return nil, configurationError()
	}
	for name := range om {
		if _, ok := lm[name]; !ok {
			return nil, configurationError()
		}
	}
	addresses := map[string]bool{}
	paths := map[string]bool{}
	infos := []os.FileInfo{}
	out := make([]Connection, 0, len(listens))
	for _, v := range listens {
		name := "default"
		if named {
			name, _, _ = strings.Cut(v, "=")
		}
		listen := lm[name]
		file, ok := fm[name]
		if !ok {
			return nil, configurationError()
		}
		host, port, err := net.SplitHostPort(listen)
		n, e := strconv.Atoi(port)
		if err != nil || e != nil || host != "127.0.0.1" || n < 1 || n > 65535 || strconv.Itoa(n) != port || addresses[listen] {
			return nil, configurationError()
		}
		addresses[listen] = true
		if err := validateWebOrigin(om[name]); err != nil {
			return nil, err
		}
		abs, err := filepath.Abs(file)
		if err != nil {
			return nil, configurationError()
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
		if err != nil {
			return nil, codeFileError()
		}
		canonical := filepath.Join(parent, filepath.Base(abs))
		if paths[canonical] {
			return nil, configurationError()
		}
		paths[canonical] = true
		if info, err := os.Stat(abs); err == nil {
			for _, previous := range infos {
				if os.SameFile(info, previous) {
					return nil, configurationError()
				}
			}
			infos = append(infos, info)
		} else if !os.IsNotExist(err) {
			return nil, codeFileError()
		}
		endpoint := "epf://" + name
		if !named {
			endpoint = "epf://local"
		}
		out = append(out, Connection{Name: name, Listen: listen, TokenFile: file, WebOrigin: om[name], Endpoint: endpoint, Legacy: !named})
	}
	return out, nil
}

// NewConnection reserves this base's private file and pins its transport endpoint.
func NewConnection(c Connection, admit func(ctx context.Context, task Task) error) (*Bridge, error) {
	expected := "epf://" + c.Name
	if c.Legacy {
		if c.Name != "default" {
			return nil, configurationError()
		}
		expected = "epf://local"
	}
	if !validName(c.Name) || c.Endpoint != expected {
		return nil, configurationError()
	}
	b, err := NewFromFile(c.TokenFile, admit, c.WebOrigin)
	if err != nil {
		return nil, err
	}
	b.endpoint = c.Endpoint
	return b, nil
}

// IsEndpoint recognizes only the canonical logical EPF endpoints, never a network URL.
func IsEndpoint(endpoint string) bool {
	return strings.HasPrefix(endpoint, "epf://") && validName(strings.TrimPrefix(endpoint, "epf://"))
}

// OwnsEndpoint binds callers to this bridge, including the original unnamed API.
func (b *Bridge) OwnsEndpoint(endpoint string) bool {
	if b == nil {
		return false
	}
	expected := b.endpoint
	if expected == "" {
		expected = "epf://local"
	}
	return endpoint == expected
}
