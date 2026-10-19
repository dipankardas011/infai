package tui

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"charm.land/lipgloss/v2"
)

const (
	// workspaceBatchSize is how many entries a scan hands over at a time, so the
	// picker can draw results before the whole tree has been read.
	workspaceBatchSize = 4096
	// workspacePickLimit is how many paths the picker shows at once.
	workspacePickLimit = 12
	// workspaceMaxFiles bounds one listing. The picker is for @ references, not
	// a full-disk index, and an unbounded home-directory listing costs gigabytes
	// of paths. A path outside this prefix can still be typed: submit resolves
	// references against the workspace itself.
	workspaceMaxFiles = 200_000
)

type filePicker struct {
	matches  []string
	query    string
	start    int
	selected int
}

// walkWorkspaceFiles streams the workspace listing in batches. Returning false
// from emit stops the walk. .git and node_modules are skipped: they are the bulk
// of a large tree and are not what an @ reference points at (fzf's default walk
// skips the same two).
func walkWorkspaceFiles(root string, emit func([]string) bool) {
	if root == "" {
		return
	}
	batch := make([]string, 0, workspaceBatchSize)
	total := 0
	stopped := false
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if stopped {
			return filepath.SkipAll
		}
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
		batch = append(batch, filepath.ToSlash(rel))
		total++
		if len(batch) >= workspaceBatchSize {
			if !emit(batch) {
				stopped = true
				return filepath.SkipAll
			}
			batch = make([]string, 0, workspaceBatchSize)
		}
		if total >= workspaceMaxFiles {
			stopped = true
			return filepath.SkipAll
		}
		return nil
	})
	if len(batch) > 0 && !stopped {
		emit(batch)
	}
}

// scanWorkspaceFiles is the eager listing for callers with no streaming
// consumer: tests, and any one-shot use.
func scanWorkspaceFiles(root string) []string {
	var files []string
	walkWorkspaceFiles(root, func(batch []string) bool {
		files = append(files, batch...)
		return true
	})
	sort.Strings(files)
	return files
}

func fuzzyFileMatches(files []string, query string, limit int) []string {
	return fuzzyFileMatchesCancelable(files, query, limit, nil)
}

// fuzzyFileMatchesCancelable matches files against query, stopping early when
// cancel is set. A cancelled scan returns nil: its caller has a newer request.
func fuzzyFileMatchesCancelable(files []string, query string, limit int, cancel *atomic.Bool) []string {
	if cancel != nil && cancel.Load() {
		return nil
	}
	// An empty query needs no scoring. The listing is in walk order, so taking
	// the head is O(limit) instead of sorting every entry.
	if query == "" {
		n := len(files)
		if limit > 0 && n > limit {
			n = limit
		}
		return files[:n]
	}
	type match struct {
		path  string
		score int
	}
	better := func(a, b match) bool {
		if a.score != b.score {
			return a.score < b.score
		}
		return len(a.path) < len(b.path)
	}
	query = strings.ToLower(query)
	var found []match
	for index, path := range files {
		if cancel != nil && index%workspaceBatchSize == 0 && cancel.Load() {
			return nil
		}
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
		if !ok {
			continue
		}
		// Keep only the best limit, sorted, instead of collecting and sorting
		// every match: a short query can match the whole listing, and the
		// caller only ever shows the head.
		if limit <= 0 {
			found = append(found, match{path, score})
			continue
		}
		at := len(found)
		for i := range found {
			if better(match{path, score}, found[i]) {
				at = i
				break
			}
		}
		if at == len(found) {
			if len(found) == limit {
				continue
			}
			found = append(found, match{path, score})
			continue
		}
		found = append(found, match{})
		copy(found[at+1:], found[at:])
		found[at] = match{path, score}
		if len(found) > limit {
			found = found[:limit]
		}
	}
	if limit <= 0 {
		sort.SliceStable(found, func(i, j int) bool { return better(found[i], found[j]) })
	}
	out := make([]string, len(found))
	for i := range found {
		out[i] = found[i].path
	}
	return out
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

// encodeFileReferences turns @path tokens in text into [file:path] blocks. A
// token is resolved against the workspace on disk, so submitting never needs a
// listing: one stat per token, wherever the message was typed.
func encodeFileReferences(text, root string) string {
	if root == "" || !strings.Contains(text, "@") {
		return text
	}
	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); {
		if text[i] != '@' {
			out.WriteByte(text[i])
			i++
			continue
		}
		end := i + 1
		for end < len(text) && !isReferenceSpace(text[end]) {
			end++
		}
		token := text[i+1 : end]
		if path := referencePath(root, token); path != "" {
			out.WriteString("[file:")
			out.WriteString(path)
			out.WriteByte(']')
			out.WriteString(token[len(path):])
		} else {
			out.WriteString(text[i:end])
		}
		i = end
	}
	return out.String()
}

func isReferenceSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}

// referencePath returns the file a token names, with a trailing punctuation mark
// left out: "read @a.go, then" is still a reference to a.go. A filename that
// itself ends in punctuation wins over the trimmed spelling.
func referencePath(root, token string) string {
	if workspaceFile(root, token) {
		return token
	}
	for len(token) > 0 && strings.ContainsRune(".,;:!?)]}'\"", rune(token[len(token)-1])) {
		token = token[:len(token)-1]
		if workspaceFile(root, token) {
			return token
		}
	}
	return ""
}

// workspaceFile reports whether path names a regular file inside root. The
// workspace is the boundary: an absolute path or a .. escape is not a reference
// the picker could have produced.
func workspaceFile(root, path string) bool {
	if path == "" || filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return false
	}
	info, err := os.Stat(filepath.Join(root, clean))
	return err == nil && info.Mode().IsRegular()
}

func encodeSkillReferences(text string) string {
	return replaceSkillReferences(text, "/skill:", "[skill:", "]", false)
}

func renderSkillReferences(text string) string {
	return replaceSkillReferences(text, "[skill:", "/skill:", "", true)
}

func replaceSkillReferences(text, from, to, suffix string, closingBracket bool) string {
	for {
		start := strings.Index(text, from)
		if start < 0 {
			return text
		}
		nameStart := start + len(from)
		end := nameStart
		for end < len(text) && (text[end] >= 'a' && text[end] <= 'z' || text[end] >= 'A' && text[end] <= 'Z' || text[end] >= '0' && text[end] <= '9' || strings.ContainsRune("._-", rune(text[end]))) {
			end++
		}
		if end == nameStart || closingBracket && (end == len(text) || text[end] != ']') {
			text = text[:start] + text[start+len(from):]
			continue
		}
		consume := end
		if closingBracket {
			consume++
		}
		text = text[:start] + to + text[nameStart:end] + suffix + text[consume:]
	}
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
