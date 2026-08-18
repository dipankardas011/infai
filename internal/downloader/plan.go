package downloader

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/dipankardas011/infai/internal/hub"
	"github.com/dipankardas011/infai/internal/model"
)

var shardPattern = regexp.MustCompile(`^(.+)-(\d{5})-of-(\d{5})\.gguf$`)

type ggufCandidate struct {
	Prefix  string
	Shards  []hub.FileEntry
	IsMulti bool
	Total   int
}

type GGUFVariant struct {
	Name       string
	Files      []hub.FileEntry
	Sharded    bool
	ShardCount int
	TotalBytes int64
}

func ListGGUFVariants(files []hub.FileEntry) []GGUFVariant {
	ggufFiles := filterByExt(files, ".gguf")
	candidates := groupGGUFCandidates(ggufFiles)

	variants := make([]GGUFVariant, 0, len(candidates))
	for _, c := range candidates {
		var total int64
		for _, f := range c.Shards {
			total += f.Size
		}
		variants = append(variants, GGUFVariant{
			Name:       c.Prefix,
			Files:      c.Shards,
			Sharded:    c.IsMulti,
			ShardCount: c.Total,
			TotalBytes: total,
		})
	}
	return variants
}

func PlanGGUFVariant(repoID, revision string, variant GGUFVariant, allFiles []hub.FileEntry) *DownloadPlan {
	var mmprojFiles []hub.FileEntry
	for _, f := range allFiles {
		if strings.HasSuffix(strings.ToLower(f.Path), ".gguf") && isMmprojFile(f.Path) {
			mmprojFiles = append(mmprojFiles, f)
		}
	}

	plan := &DownloadPlan{
		RepoID:     repoID,
		Revision:   revision,
		EngineKind: model.EngineLlamaCPP,
		Files:      ToPlanFiles(variant.Files),
	}
	if len(mmprojFiles) > 0 {
		plan.OptionalFiles = ToPlanFiles(mmprojFiles)
	}
	plan.TotalBytes = plan.CombinedBytes()
	return plan
}

func PlanFiles(repoID, revision string, files []hub.FileEntry, engine model.EngineKind) (*DownloadPlan, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("no files to plan")
	}
	switch engine {
	case model.EngineLlamaCPP:
		return planGGUF(repoID, revision, files)
	case model.EngineVLLM:
		return planSafetensors(repoID, revision, files)
	default:
		return nil, fmt.Errorf("unsupported engine kind %q", engine)
	}
}

func PlanSafetensorsWithIndex(repoID, revision string, files []hub.FileEntry, indexContent []byte) (*DownloadPlan, error) {
	plan := baseVLLMPlan(repoID, revision, files)
	if plan == nil {
		return nil, fmt.Errorf("config.json is required but not found")
	}

	shardPaths, err := parseWeightMap(indexContent)
	if err != nil {
		return nil, fmt.Errorf("model index: %w", err)
	}

	byPath := indexByPath(files)
	var shards []hub.FileEntry
	var missing []string
	for _, sp := range shardPaths {
		if f, ok := byPath[sp]; ok {
			shards = append(shards, f)
		} else {
			missing = append(missing, sp)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing safetensor shards referenced by index: %s", strings.Join(missing, ", "))
	}

	indexEntry := findByPath(files, "model.safetensors.index.json")
	if indexEntry != nil {
		shards = append([]hub.FileEntry{*indexEntry}, shards...)
	}

	plan.Files = append(plan.Files, ToPlanFiles(shards)...)
	plan.TotalBytes = plan.CombinedBytes()
	return plan, nil
}

func parseWeightMap(content []byte) ([]string, error) {
	var idx struct {
		WeightMap map[string]string `json:"weight_map"`
	}
	if err := json.Unmarshal(content, &idx); err != nil {
		return nil, err
	}
	if len(idx.WeightMap) == 0 {
		return nil, fmt.Errorf("weight_map is empty")
	}
	seen := make(map[string]bool)
	var paths []string
	for _, p := range idx.WeightMap {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	return paths, nil
}

func planGGUF(repoID, revision string, files []hub.FileEntry) (*DownloadPlan, error) {
	ggufFiles := filterByExt(files, ".gguf")
	if len(ggufFiles) == 0 {
		return nil, fmt.Errorf("no GGUF files found in repository")
	}

	candidates := groupGGUFCandidates(ggufFiles)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no GGUF candidates found")
	}

	var mmprojFiles []hub.FileEntry
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(f.Path), ".gguf") && isMmprojFile(f.Path) {
			mmprojFiles = append(mmprojFiles, f)
		}
	}

	candidate := candidates[0]
	required := make([]hub.FileEntry, len(candidate.Shards))
	copy(required, candidate.Shards)

	plan := &DownloadPlan{
		RepoID:     repoID,
		Revision:   revision,
		EngineKind: model.EngineLlamaCPP,
		Files:      ToPlanFiles(required),
	}

	if len(mmprojFiles) > 0 {
		plan.OptionalFiles = ToPlanFiles(mmprojFiles)
	}

	plan.TotalBytes = plan.CombinedBytes()
	return plan, nil
}

func planSafetensors(repoID, revision string, files []hub.FileEntry) (*DownloadPlan, error) {
	plan := baseVLLMPlan(repoID, revision, files)
	if plan == nil {
		return nil, fmt.Errorf("config.json is required but not found")
	}

	sfFiles := filterByExt(files, ".safetensors")
	if len(sfFiles) == 0 {
		return nil, fmt.Errorf("no .safetensors files found")
	}

	plan.Files = append(plan.Files, ToPlanFiles(sfFiles)...)
	plan.TotalBytes = plan.CombinedBytes()
	return plan, nil
}

func baseVLLMPlan(repoID, revision string, files []hub.FileEntry) *DownloadPlan {
	configFile := findByPath(files, "config.json")
	if configFile == nil {
		return nil
	}
	tokenizerFiles := findTokenizerFiles(files)
	required := []hub.FileEntry{*configFile}
	required = append(required, tokenizerFiles...)

	return &DownloadPlan{
		RepoID:     repoID,
		Revision:   revision,
		EngineKind: model.EngineVLLM,
		Files:      ToPlanFiles(required),
	}
}

func groupGGUFCandidates(files []hub.FileEntry) []ggufCandidate {
	shardGroups := make(map[string][]hub.FileEntry)
	var singles []hub.FileEntry

	for _, f := range files {
		base := path.Base(f.Path)
		m := shardPattern.FindStringSubmatch(base)
		if m != nil {
			prefix := m[1]
			shardGroups[prefix] = append(shardGroups[prefix], f)
		} else if !isMmprojFile(f.Path) {
			singles = append(singles, f)
		}
	}

	var candidates []ggufCandidate

	for prefix, shards := range shardGroups {
		sort.Slice(shards, func(i, j int) bool {
			return shards[i].Path < shards[j].Path
		})
		candidates = append(candidates, ggufCandidate{
			Prefix:  prefix,
			Shards:  shards,
			IsMulti: true,
			Total:   len(shards),
		})
	}

	for _, f := range singles {
		candidates = append(candidates, ggufCandidate{
			Prefix:  strings.TrimSuffix(path.Base(f.Path), ".gguf"),
			Shards:  []hub.FileEntry{f},
			IsMulti: false,
			Total:   1,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Prefix < candidates[j].Prefix
	})

	return candidates
}

func filterByExt(files []hub.FileEntry, ext string) []hub.FileEntry {
	var out []hub.FileEntry
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(f.Path), ext) {
			out = append(out, f)
		}
	}
	return out
}

func findByPath(files []hub.FileEntry, target string) *hub.FileEntry {
	for _, f := range files {
		if f.Path == target {
			return &f
		}
	}
	return nil
}

func indexByPath(files []hub.FileEntry) map[string]hub.FileEntry {
	m := make(map[string]hub.FileEntry, len(files))
	for _, f := range files {
		m[f.Path] = f
	}
	return m
}

func isMmprojFile(filepath string) bool {
	return strings.Contains(strings.ToLower(filepath), "mmproj")
}

func findTokenizerFiles(files []hub.FileEntry) []hub.FileEntry {
	names := map[string]bool{
		"tokenizer.json":          true,
		"tokenizer_config.json":   true,
		"special_tokens_map.json": true,
		"vocab.json":              true,
		"merges.txt":              true,
		"added_tokens.json":       true,
		"chat_template.jinja":     true,
	}
	var out []hub.FileEntry
	for _, f := range files {
		if names[f.Path] {
			out = append(out, f)
		}
	}
	return out
}

func sumBytes(files []PlanFile) int64 {
	var total int64
	for _, f := range files {
		total += f.Size
	}
	return total
}
