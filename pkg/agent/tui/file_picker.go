package tui

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
)

type filePicker struct {
	files    []string
	matches  []string
	query    string
	start    int
	selected int
}

func scanWorkspaceFiles(root string) []string {
	if root == "" {
		return nil
	}
	var files []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(files)
	return files
}

func fuzzyFileMatches(files []string, query string, limit int) []string {
	type match struct {
		path  string
		score int
	}
	query = strings.ToLower(query)
	var found []match
	for _, path := range files {
		lower, pos, score := strings.ToLower(path), 0, 0
		ok := true
		for _, char := range query {
			i := strings.IndexRune(lower[pos:], char)
			if i < 0 {
				ok = false
				break
			}
			pos += i + 1
			score += i
		}
		if ok {
			found = append(found, match{path, score})
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].score != found[j].score {
			return found[i].score < found[j].score
		}
		return len(found[i].path) < len(found[j].path)
	})
	if limit > 0 && len(found) > limit {
		found = found[:limit]
	}
	out := make([]string, len(found))
	for i := range found {
		out[i] = found[i].path
	}
	return out
}

func (p *filePicker) filter(query string) {
	p.query = query
	p.matches = fuzzyFileMatches(p.files, query, 12)
	p.selected = min(p.selected, max(len(p.matches)-1, 0))
}

func renderFilePicker(p *filePicker, width int, styles harnessStyles) string {
	if p == nil || len(p.matches) == 0 {
		return ""
	}
	menuWidth := 0
	for _, path := range p.matches {
		menuWidth = max(menuWidth, lipgloss.Width("  "+path))
	}
	menuWidth = min(menuWidth+styles.menu.GetHorizontalFrameSize(), width)
	innerWidth := contentWidth(styles.menu, menuWidth)
	rows := make([]string, 0, len(p.matches))
	for i, path := range p.matches {
		prefix := "  "
		style := styles.menuRow
		if i == p.selected {
			prefix = "› "
			style = styles.menuActive
		}
		rows = append(rows, style.Width(innerWidth).Render(prefix+path))
	}
	return styles.menu.Width(menuWidth).Render(strings.Join(rows, "\n"))
}

func encodeFileReferences(text string, files []string) string {
	sort.Slice(files, func(i, j int) bool { return len(files[i]) > len(files[j]) })
	for _, path := range files {
		text = strings.ReplaceAll(text, "@"+path, "[file:"+path+"]")
	}
	return text
}

func renderFileReferences(text string) string {
	for {
		start := strings.Index(text, "[file:")
		if start < 0 {
			return text
		}
		end := strings.IndexByte(text[start:], ']')
		if end < 0 {
			return text
		}
		end += start
		path := text[start+6 : end]
		text = text[:start] + "@" + path + text[end+1:]
	}
}
