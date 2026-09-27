package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
)

func editComposerCmd(draft string) tea.Cmd {
	return func() tea.Msg {
		editor := os.Getenv("EDITOR")
		if editor == "" {
			return editorDoneMsg{err: errors.New("$EDITOR is not set")}
		}
		dir, err := os.MkdirTemp("", "infai-compose-*")
		if err != nil {
			return editorDoneMsg{err: err}
		}

		path := filepath.Join(dir, "message.md")
		if err := os.WriteFile(path, []byte(draft), 0o600); err != nil {
			return editorDoneMsg{err: err}
		}
		parts := strings.Fields(editor)
		if len(parts) == 0 {
			return editorDoneMsg{err: errors.New("$EDITOR is empty")}
		}
		cmd := exec.Command(parts[0], append(parts[1:], path)...)
		return tea.ExecProcess(cmd, func(err error) tea.Msg {
			defer os.RemoveAll(dir)
			if err != nil {
				return editorDoneMsg{err: err}
			}
			text, readErr := os.ReadFile(path)
			return editorDoneMsg{text: string(text), err: readErr}
		})()
	}
}
