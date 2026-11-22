package browserext

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// assetsRoot is where go:embed put the extension inside this package.
const assetsRoot = "assets"

// configFile is the one asset the caller replaces: it carries what the
// extension needs to find its daemon.
const configFile = "config.json"

// extensionName is how a folder proves it is ours before we rewrite it.
const extensionName = "infai browser"

// Config is what the extension needs baked in at install time. The profile
// label is not part of it: the side panel stores that, because only the user
// knows which Chrome profile they loaded the extension into.
type Config struct {
	DaemonURL string `json:"daemon_url"`
	Token     string `json:"token"`
}

// ExtensionID is the id Chrome assigns an unpacked extension: the first 16
// bytes of SHA-256 over its absolute path, with each hex digit shifted into
// a-p. § chrome/browser/extensions/unpacked_installer + extension_id.h
func ExtensionID(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve extension dir: %w", err)
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	var id [32]byte
	for i, b := range sum[:16] {
		id[2*i] = 'a' + b>>4
		id[2*i+1] = 'a' + b&0x0f
	}
	return string(id[:]), nil
}

// Install writes the embedded extension into dir, replacing files whose
// content changed and removing files this build no longer ships. It refuses to
// touch a folder that is not empty and not one of ours.
func Install(dir string, cfg Config) (changed bool, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false, fmt.Errorf("resolve extension dir: %w", err)
	}
	if err := checkInstallTarget(abs); err != nil {
		return false, err
	}

	configJSON, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encode extension config: %w", err)
	}
	configJSON = append(configJSON, '\n')

	if err := os.MkdirAll(abs, 0o755); err != nil {
		return false, fmt.Errorf("create %s: %w", abs, err)
	}

	expected := make(map[string]struct{})
	err = fs.WalkDir(Assets, assetsRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := strings.TrimPrefix(p, assetsRoot+"/")
		if name == p {
			return fmt.Errorf("unexpected asset path %q", p)
		}
		data := configJSON
		if name != configFile {
			if data, err = Assets.ReadFile(p); err != nil {
				return err
			}
		}
		expected[name] = struct{}{}
		wrote, err := writeIfChanged(filepath.Join(abs, filepath.FromSlash(name)), data)
		changed = changed || wrote
		return err
	})
	if err != nil {
		return changed, fmt.Errorf("install extension into %s: %w", abs, err)
	}

	removed, err := pruneStale(abs, expected)
	return changed || removed, err
}

// checkInstallTarget refuses a non-empty folder that is not this extension, so
// a mistyped path can never be emptied.
func checkInstallTarget(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	if len(entries) == 0 {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return fmt.Errorf("refusing to install into %s: it is not empty and has no manifest.json", dir)
	}
	var manifest struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.Name != extensionName {
		return fmt.Errorf("refusing to install into %s: it does not look like the infai browser extension", dir)
	}
	return nil
}

// pruneStale removes files this build does not ship, deepest first, so an
// upgrade cannot leave code behind that the current manifest no longer names.
func pruneStale(dir string, expected map[string]struct{}) (bool, error) {
	var dirs []string
	removed := false
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		if _, keep := expected[filepath.ToSlash(rel)]; keep {
			return nil
		}
		if err := os.Remove(p); err != nil {
			return fmt.Errorf("remove stale %s: %w", p, err)
		}
		removed = true
		return nil
	})
	if err != nil {
		return removed, err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if entries, err := os.ReadDir(dirs[i]); err == nil && len(entries) == 0 {
			_ = os.Remove(dirs[i])
		}
	}
	return removed, nil
}

// writeIfChanged writes data atomically and reports whether the file needed it.
func writeIfChanged(dst string, data []byte) (bool, error) {
	if current, err := os.ReadFile(dst); err == nil && string(current) == string(data) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
	}
	temp, err := os.CreateTemp(filepath.Dir(dst), ".infai-ext-*")
	if err != nil {
		return false, fmt.Errorf("write %s: %w", dst, err)
	}
	name := temp.Name()
	defer os.Remove(name)
	if err = temp.Chmod(0o644); err == nil {
		_, err = temp.Write(data)
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, dst)
	}
	if err != nil {
		return false, fmt.Errorf("write %s: %w", dst, err)
	}
	return true, nil
}
