// Memory houses the agent's knowledge layers: authored skills (curated
// procedures) and, later, episodic memory (learned facts). Skills are the
// deterministic layer — scanned from disk at session start and loaded by
// exact name via read_skill, never by free-path traversal.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"gopkg.in/yaml.v3"
)

// skillsDir is the conventional skill root under both the home and project dirs.
const skillsDir = ".agents/skills"

// maxSkillBytes bounds SKILL.md reads at scan and load time. Skill contents
// are returned directly to the model, so they use the tool response budget.
const maxSkillBytes = 24 << 10

var skillNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// SkillRegistry indexes authored skills (SKILL.md with YAML frontmatter) from
// the home root (personal base) and the project root. A same-named project
// skill wins over the home one. ReadSkill resolves names against this map only,
// so a skill call can never read an arbitrary file.
type SkillRegistry struct {
	byName map[string]contracts.Skill
}

// LoadSkillRegistry scans <home>/.agents/skills then <project>/.agents/skills.
// Roots that do not exist or equal an earlier root are skipped.
func LoadSkillRegistry(projectDir string) (*SkillRegistry, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir for skills: %w", err)
	}

	r := &SkillRegistry{byName: make(map[string]contracts.Skill)}
	seen := make(map[string]struct{})
	for _, root := range []string{homeDir, projectDir} {
		if root == "" {
			continue
		}
		if _, dup := seen[root]; dup {
			continue
		}
		seen[root] = struct{}{}
		if err := r.scanRoot(filepath.Join(root, skillsDir)); err != nil {
			return nil, fmt.Errorf("scan skills at %s: %w", root, err)
		}
	}
	return r, nil
}

func (r *SkillRegistry) scanRoot(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path, ok := resolveWithin(root, filepath.Join(root, entry.Name(), "SKILL.md"))
		if !ok {
			continue
		}
		content, err := readCapped(path)
		if err != nil {
			continue
		}
		fm, ok := parseFrontmatter(content)
		if !ok {
			continue
		}
		name := strings.TrimSpace(fm.Name)
		if name == "" {
			name = entry.Name()
		}
		if !skillNameRe.MatchString(name) {
			continue
		}
		r.byName[name] = contracts.Skill{
			Title:       name,
			Description: strings.TrimSpace(fm.Description),
			Location:    path,
		}
	}
	return nil
}

// resolveWithin returns the canonical path of file after resolving symlinks,
// provided it stays inside root and is a regular file. Rejects symlinked
// SKILL.md files that point outside the skill root.
func resolveWithin(root, file string) (string, bool) {
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(file)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(rootResolved, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return resolved, true
}

type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// parseFrontmatter extracts the YAML block between leading --- markers.
// Tolerates a UTF-8 BOM and CRLF line endings.
func parseFrontmatter(content []byte) (skillFrontmatter, bool) {
	text := string(content)
	text = strings.TrimPrefix(text, "\xef\xbb\xbf")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return skillFrontmatter{}, false
	}
	rest := text[4:]
	before, _, ok := strings.Cut(rest, "\n---")
	if !ok {
		return skillFrontmatter{}, false
	}
	var fm skillFrontmatter
	if err := yaml.Unmarshal([]byte(before), &fm); err != nil {
		return skillFrontmatter{}, false
	}
	return fm, true
}

// readCapped reads a file bounded by maxSkillBytes and validates UTF-8.
func readCapped(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSkillBytes {
		return nil, fmt.Errorf("skill file not a regular file within size bound: %s", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(content) > maxSkillBytes || !utf8.Valid(content) {
		return nil, fmt.Errorf("skill file exceeds bound or is not UTF-8: %s", path)
	}
	return content, nil
}

// Skills returns the registry as the model-visible index. Location is carried
// for tool resolution but must not be rendered into the system prompt.
func (r *SkillRegistry) Skills() []contracts.Skill {
	out := make([]contracts.Skill, 0, len(r.byName))
	for _, skill := range r.byName {
		out = append(out, skill)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

// ReadSkill returns the raw SKILL.md body for the named skill. The stored
// location is canonical (symlink-resolved at scan); it is re-checked so a
// later symlink swap cannot redirect the read outside the registry.
func (r *SkillRegistry) ReadSkill(name string) (string, error) {
	skill, ok := r.byName[name]
	if !ok {
		return "", fmt.Errorf("unknown skill %q", name)
	}
	info, err := os.Lstat(skill.Location)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("skill %q no longer a regular file", name)
	}
	content, err := readCapped(skill.Location)
	if err != nil {
		return "", fmt.Errorf("read skill %q: %w", name, err)
	}
	return string(content), nil
}
