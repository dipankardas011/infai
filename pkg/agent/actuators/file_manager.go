package actuators

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FileManager is the file capability for one session. Snapshots are deliberately
// session-local: a write may only replace content the agent has observed.
type FileManager struct {
	root string
	mu   sync.Mutex
	seen map[string]string
}

func NewFileManager(root string) (*FileManager, error) {
	if root == "" {
		return nil, filesystemErr("invalid_workspace", "the workspace path must not be empty", ResponsibilitySession, nil)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, filesystemErr("invalid_workspace", "the workspace path is invalid", ResponsibilitySession, err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, filesystemErr("workspace_unavailable", "the workspace could not be resolved", ResponsibilityEnvironment, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, filesystemErr("workspace_unavailable", "the workspace could not be inspected", ResponsibilityEnvironment, err)
	}
	if !info.IsDir() {
		return nil, filesystemErr("workspace_not_directory", "the workspace is not a directory", ResponsibilitySession, nil)
	}
	return &FileManager{root: root, seen: make(map[string]string)}, nil
}

func (m *FileManager) Root() string { return m.root }

func (m *FileManager) resolve(name string, mustExist bool) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.ContainsRune(name, 0) {
		return "", filesystemErr("invalid_path", "the path must be relative to the workspace", ResponsibilityAgent, nil)
	}
	if err := validateText(name, ResponsibilityAgent); err != nil {
		return "", err
	}
	candidate := filepath.Join(m.root, filepath.Clean(name))
	if !withinDirectory(m.root, candidate) {
		return "", filesystemErr("path_outside_workspace", "the path must remain inside the workspace", ResponsibilityAgent, nil)
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if !mustExist && os.IsNotExist(err) {
			parent, parentErr := filepath.EvalSymlinks(filepath.Dir(candidate))
			if parentErr != nil {
				return "", filesystemErr("file_unavailable", "the parent directory could not be resolved", ResponsibilityEnvironment, parentErr)
			}
			if !withinDirectory(m.root, parent) {
				return "", filesystemErr("path_outside_workspace", "the path parent must remain inside the workspace", ResponsibilityAgent, nil)
			}
			return filepath.Join(parent, filepath.Base(candidate)), nil
		}
		return "", filesystemErr("file_unavailable", "the path could not be resolved", ResponsibilityEnvironment, err)
	}
	if !withinDirectory(m.root, resolved) {
		return "", filesystemErr("path_outside_workspace", "the path symlink must remain inside the workspace", ResponsibilityAgent, nil)
	}
	return resolved, nil
}

func hashBytes(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func (m *FileManager) snapshot(path string, data []byte) { m.seen[path] = hashBytes(data) }
func (m *FileManager) verify(path string, data []byte) error {
	actual := hashBytes(data)
	if observed, ok := m.seen[path]; !ok || observed != actual {
		return filesystemErr(
			"file_changed_since_read",
			"the file must be read before it can be changed, and it must not change afterwards",
			ResponsibilityAgent,
			nil,
		)
	}
	return nil
}

type fileManagerKey struct{}

func WithFileManager(ctx context.Context, manager *FileManager) context.Context {
	return context.WithValue(ctx, fileManagerKey{}, manager)
}
func FileManagerFromContext(ctx context.Context) *FileManager {
	manager, _ := ctx.Value(fileManagerKey{}).(*FileManager)
	return manager
}
