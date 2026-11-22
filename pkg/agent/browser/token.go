package browser

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const tokenFile = "browser_token"

// LoadOrCreateToken returns the shared secret the extension must present,
// generating and persisting a new one under dir on first use.
func LoadOrCreateToken(dir string) (string, error) {
	path := filepath.Join(dir, tokenFile)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if tok := strings.TrimSpace(string(data)); tok != "" {
			return tok, nil
		}
	case !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("read browser token: %w", err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create token dir: %w", err)
	}

	buf := make([]byte, 32)
	rand.Read(buf) // never returns an error since Go 1.24; it crashes instead
	tok := hex.EncodeToString(buf)

	// CreateTemp opens with 0600, and rename keeps a reader from seeing a partial token.
	f, err := os.CreateTemp(dir, tokenFile+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("create browser token: %w", err)
	}
	tmp := f.Name()
	if _, err := f.WriteString(tok); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", fmt.Errorf("write browser token: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("write browser token: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("save browser token: %w", err)
	}
	return tok, nil
}
