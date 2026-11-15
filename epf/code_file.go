package epf

import (
	"context"
	"errors"
	"io"
	"time"
)

const codeFileHeader = "MCP-EPF-ONE-TIME-CODE/1\n"
const pairingLifetime = 5 * time.Minute

func codeFileError() error {
	return errors.New("EPF code file unavailable: use a dedicated server-managed private regular file (Unix 0600; Windows protected current-user-only DACL), close other issuers, fix the path and restart MCP; do not reuse an old input-token file")
}

// NewFromFile reserves a dedicated server-managed delivery file. Start issues its code.
// Existing operator input files are never adopted or overwritten.
func NewFromFile(path string, admit func(context.Context, Task) error, webOrigin ...string) (*Bridge, error) {
	b, err := New(randomToken(32), admit, webOrigin...)
	if err != nil {
		return nil, err
	}
	f, created, err := openCodeFile(path)
	if err != nil {
		return nil, err
	}
	if !created {
		data, readErr := io.ReadAll(io.LimitReader(f, int64(len(codeFileHeader)+66)))
		if readErr != nil || len(data) != len(codeFileHeader)+65 || string(data[:len(codeFileHeader)]) != codeFileHeader || data[len(data)-1] != '\n' {
			f.Close()
			return nil, codeFileError()
		}
		if _, err := New(string(data[len(codeFileHeader):len(data)-1]), nil); err != nil {
			f.Close()
			return nil, codeFileError()
		}
	}
	b.pairing = ""
	b.pairingExpires = time.Time{}
	b.codeFile, b.codePath = f, path
	if created {
		// Persist a recognized envelope for retry; no pairing is active before Start.
		if err := b.issueCode(); err != nil {
			b.Close()
			return nil, err
		}
		b.pairing = ""
		b.pairingExpires = time.Time{}
	}
	return b, nil
}

// Caller holds b.mu; publish only after the complete private write is durable.
func (b *Bridge) issueCode() error {
	b.pairing = ""
	b.pairingExpires = time.Time{}
	code := randomToken(32)
	if b.codeFile != nil {
		if validateCodeFile(b.codeFile, b.codePath) != nil {
			return codeFileError()
		}
		data := []byte(codeFileHeader + code + "\n")
		n, err := b.codeFile.WriteAt(data, 0)
		if err != nil || n != len(data) || b.codeFile.Truncate(int64(len(data))) != nil || b.codeFile.Sync() != nil || validateCodeFile(b.codeFile, b.codePath) != nil {
			return codeFileError()
		}
	}
	b.pairing, b.pairingExpires = code, time.Now().Add(pairingLifetime)
	return nil
}
