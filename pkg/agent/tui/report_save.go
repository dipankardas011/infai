package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"
)

// reportSavedMsg reports the outcome of /report.
type reportSavedMsg struct {
	path string
	err  error
}

// saveReportCmd fetches the session's HTML report and writes it into dir.
func saveReportCmd(ctx context.Context, client Client, id uuid.UUID, dir string) tea.Cmd {
	return func() tea.Msg {
		if dir == "" {
			return reportSavedMsg{err: errors.New("the session workspace is unknown")}
		}
		page, err := client.GetReport(ctx, id)
		if err != nil {
			return reportSavedMsg{err: err}
		}
		path, err := writeReport(dir, id, page)
		return reportSavedMsg{path: path, err: err}
	}
}

// writeReport never overwrites: O_EXCL makes a name taken between the free-name
// probe and the write a failure rather than a clobber.
func writeReport(dir string, id uuid.UUID, page []byte) (string, error) {
	base := fmt.Sprintf("infai-report-%s-%s", id, time.Now().UTC().Format("20060102-150405"))
	path := freeGeneratedName(dir, base, ".html")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("save report: %w", err)
	}
	if _, err := f.Write(page); err != nil {
		f.Close()
		return "", fmt.Errorf("save report: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("save report: %w", err)
	}
	return path, nil
}
