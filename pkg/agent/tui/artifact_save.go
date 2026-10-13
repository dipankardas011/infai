package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// attachArtifacts puts the images a turn produced on the block its text landed
// in, so a turn stays one block whether it replays from history or arrives live.
// A turn that produced no text gets a block of its own.
func (m *chatModel) attachArtifacts(artifacts []contracts.Artifact) {
	if len(artifacts) == 0 {
		return
	}
	if m.turnTextBlock >= 0 && m.turnTextBlock < len(m.blocks) && m.blocks[m.turnTextBlock].role == "assistant" {
		entry := &m.blocks[m.turnTextBlock]
		entry.artifacts, entry.artifactCount = artifacts, len(artifacts)
		entry.renderedValid = false
		return
	}
	m.blocks = append(m.blocks, block{role: "assistant", artifacts: artifacts, artifactCount: len(artifacts)})
}

// badgeHit is one on-screen [Gen Image N] chip: the block it belongs to, the
// viewport content line it sits on, and its display columns.
type badgeHit struct {
	block int
	row   int
	start int
	end   int
}

// recordBadgeHits remembers where the assembled window put every artifact chip,
// so a click can be matched against it. Rows are content lines; transcriptOffset
// turns one into a screen row.
func (m *chatModel) recordBadgeHits(window transcriptWindow) {
	m.badgeHits = m.badgeHits[:0]
	for _, entry := range window.entries {
		if entry.block < 0 || entry.block >= len(m.blocks) || entry.lines == 0 {
			continue
		}
		block := &m.blocks[entry.block]
		if block.role != "assistant" || block.artifactCount <= 0 {
			continue
		}
		row := entry.start + entry.lines - 1
		column := lipgloss.Width("●") + 1
		for i := range block.artifactCount {
			width := lipgloss.Width(fmt.Sprintf("[%s %d]", artifactBadgeLabel, i+1))
			m.badgeHits = append(m.badgeHits, badgeHit{block: entry.block, row: row, start: column, end: column + width})
			column += width + 1
		}
	}
}

// badgeAt returns the artifact chip under a screen position.
func (m *chatModel) badgeAt(x, y int) (badgeHit, bool) {
	if len(m.areas) <= 1 {
		return badgeHit{}, false
	}
	area := m.areas[1]
	row := m.transcriptOffset + (y - area.y)
	column := x - m.viewport.Style.GetPaddingLeft()
	for _, hit := range m.badgeHits {
		if hit.row == row && column >= hit.start && column < hit.end {
			return hit, true
		}
	}
	return badgeHit{}, false
}

// saveArtifactsCmd writes the clicked block's images into the session workspace.
func (m *chatModel) saveArtifactsCmd(hit badgeHit) tea.Cmd {
	if hit.block < 0 || hit.block >= len(m.blocks) {
		return nil
	}
	artifacts, dir := m.blocks[hit.block].artifacts, m.session.Cwd
	return func() tea.Msg {
		if len(artifacts) == 0 {
			return artifactSavedMsg{err: errors.New("this turn carried no image to save")}
		}
		if dir == "" {
			return artifactSavedMsg{err: errors.New("the session workspace is unknown")}
		}
		paths, err := writeGeneratedImages(dir, artifacts)
		return artifactSavedMsg{paths: paths, err: err}
	}
}

// writeGeneratedImages saves model-produced images into dir, never overwriting
// an existing file: a name already on disk takes a counter suffix.
func writeGeneratedImages(dir string, artifacts []contracts.Artifact) ([]string, error) {
	stamp := time.Now().UTC().Format("20060102-150405")
	written := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		if len(artifact.Data) == 0 {
			continue
		}
		path := freeGeneratedName(dir, "Generated-Image-"+stamp, generatedImageExt(artifact.MediaType))
		if err := os.WriteFile(path, artifact.Data, 0o644); err != nil {
			return written, fmt.Errorf("save generated image: %w", err)
		}
		written = append(written, path)
	}
	if len(written) == 0 {
		return nil, errors.New("the generated image carried no bytes")
	}
	return written, nil
}

func freeGeneratedName(dir, base, ext string) string {
	path := filepath.Join(dir, base+ext)
	for i := 2; ; i++ {
		_, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return path
		}
		if err != nil {
			return path
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, i, ext))
	}
}

func generatedImageExt(mediaType string) string {
	switch mediaType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	if subtype, ok := strings.CutPrefix(mediaType, "image/"); ok && subtype != "" {
		return "." + subtype
	}
	return ".bin"
}

// artifactSavedMsg reports the outcome of a click-to-save.
type artifactSavedMsg struct {
	paths []string
	err   error
}

// relativePaths trims the session workspace prefix, so a saved path reads short
// in the transcript.
func (m *chatModel) relativePaths(paths []string) string {
	shown := make([]string, 0, len(paths))
	for _, path := range paths {
		if m.session.Cwd != "" {
			if rel, err := filepath.Rel(m.session.Cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
				path = rel
			}
		}
		shown = append(shown, path)
	}
	return strings.Join(shown, ", ")
}
