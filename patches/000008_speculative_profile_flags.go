package patches

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

type speculativePatch struct {
	mode       string
	draftID    *int64
	tokens     int
	extraFlags string
}

func m0008(db *sql.DB) error {
	modelIDs, err := speculativeModelIDs(db)
	if err != nil {
		return err
	}
	rows, err := db.Query(`
		SELECT p.id, COALESCE(ie.kind, 'llamacpp'), p.extra_flags
		FROM profiles p
		LEFT JOIN inference_engine ie ON p.inference_engine_id = ie.id
		WHERE p.speculative_mode = '' AND p.extra_flags != ''`)
	if err != nil {
		return fmt.Errorf("m0008 query profiles: %w", err)
	}
	type update struct {
		id int64
		speculativePatch
	}
	var updates []update
	for rows.Next() {
		var id int64
		var kind, extraFlags string
		if err := rows.Scan(&id, &kind, &extraFlags); err != nil {
			rows.Close()
			return fmt.Errorf("m0008 scan profile: %w", err)
		}
		patch, ok := parseSpeculativeProfile(kind, extraFlags, modelIDs)
		if ok {
			updates = append(updates, update{id: id, speculativePatch: patch})
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("m0008 close profiles: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("m0008 profile rows: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("m0008 begin: %w", err)
	}
	defer tx.Rollback()
	for _, update := range updates {
		if _, err := tx.Exec(`UPDATE profiles SET speculative_mode=?, draft_model_id=?, speculative_tokens=?, extra_flags=? WHERE id=?`,
			update.mode, update.draftID, update.tokens, update.extraFlags, update.id); err != nil {
			return fmt.Errorf("m0008 update profile %d: %w", update.id, err)
		}
	}
	return tx.Commit()
}

func speculativeModelIDs(db *sql.DB) (map[string]int64, error) {
	rows, err := db.Query(`SELECT id, model_dir, primary_file FROM model_registry`)
	if err != nil {
		return nil, fmt.Errorf("m0008 query models: %w", err)
	}
	defer rows.Close()
	ids := make(map[string]int64)
	for rows.Next() {
		var id int64
		var dir, file string
		if err := rows.Scan(&id, &dir, &file); err != nil {
			return nil, fmt.Errorf("m0008 scan model: %w", err)
		}
		path := dir
		if file != "" {
			path = filepath.Join(dir, file)
		}
		ids[filepath.Clean(path)] = id
	}
	return ids, rows.Err()
}

func parseSpeculativeProfile(kind, extraFlags string, modelIDs map[string]int64) (speculativePatch, bool) {
	args, err := parsePatchArgs(extraFlags)
	if err != nil {
		return speculativePatch{}, false
	}
	if kind == "vllm" {
		return parseVLLMSpeculative(args, modelIDs)
	}
	return parseLlamaSpeculative(args, modelIDs)
}

func parseLlamaSpeculative(args []string, modelIDs map[string]int64) (speculativePatch, bool) {
	typeValue, typeAt, typeLen := patchFlagValue(args, "--spec-type")
	if typeAt < 0 || (typeValue != "draft-mtp" && typeValue != "draft-simple") {
		return speculativePatch{}, false
	}
	tokens := 3
	tokensValue, tokensAt, tokensLen := patchFlagValue(args, "--spec-draft-n-max")
	if tokensAt >= 0 {
		parsed, err := strconv.Atoi(tokensValue)
		if err != nil || parsed <= 0 {
			return speculativePatch{}, false
		}
		tokens = parsed
	}
	draftPath, draftAt, draftLen := patchFlagValue(args, "--spec-draft-model")
	var draftID *int64
	mode := "native_mtp"
	if draftAt >= 0 {
		id, ok := modelIDs[filepath.Clean(draftPath)]
		if !ok {
			return speculativePatch{}, false
		}
		draftID = &id
		if typeValue == "draft-mtp" {
			mode = "mtp_assistant"
		} else {
			mode = "draft_model"
		}
	} else if typeValue != "draft-mtp" {
		return speculativePatch{}, false
	}
	remove := map[int]int{typeAt: typeLen}
	if tokensAt >= 0 {
		remove[tokensAt] = tokensLen
	}
	if draftAt >= 0 {
		remove[draftAt] = draftLen
	}
	return speculativePatch{mode: mode, draftID: draftID, tokens: tokens, extraFlags: remainingPatchArgs(args, remove)}, true
}

func parseVLLMSpeculative(args []string, modelIDs map[string]int64) (speculativePatch, bool) {
	raw, at, length := patchFlagValue(args, "--speculative-config")
	if at < 0 {
		return speculativePatch{}, false
	}
	var config struct {
		Method string `json:"method"`
		Model  string `json:"model"`
		Tokens int    `json:"num_speculative_tokens"`
	}
	if json.Unmarshal([]byte(raw), &config) != nil || config.Tokens <= 0 {
		return speculativePatch{}, false
	}
	patch := speculativePatch{tokens: config.Tokens}
	switch config.Method {
	case "mtp":
		patch.mode = "native_mtp"
		if config.Model != "" {
			id, ok := modelIDs[filepath.Clean(config.Model)]
			if !ok {
				return speculativePatch{}, false
			}
			patch.mode = "mtp_assistant"
			patch.draftID = &id
		}
	case "draft_model":
		id, ok := modelIDs[filepath.Clean(config.Model)]
		if !ok {
			return speculativePatch{}, false
		}
		patch.mode = "draft_model"
		patch.draftID = &id
	default:
		return speculativePatch{}, false
	}
	patch.extraFlags = remainingPatchArgs(args, map[int]int{at: length})
	return patch, true
}

func patchFlagValue(args []string, name string) (string, int, int) {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1], i, 2
		}
		if value, ok := strings.CutPrefix(arg, name+"="); ok {
			return value, i, 1
		}
	}
	return "", -1, 0
}

func remainingPatchArgs(args []string, remove map[int]int) string {
	remaining := make([]string, 0, len(args))
	for i := 0; i < len(args); {
		if length := remove[i]; length > 0 {
			i += length
			continue
		}
		remaining = append(remaining, quotePatchArg(args[i]))
		i++
	}
	return strings.Join(remaining, " ")
}

func quotePatchArg(arg string) string {
	if arg != "" && !strings.ContainsAny(arg, " \t\n\\\"'") {
		return arg
	}
	return strconv.Quote(arg)
}

func parsePatchArgs(value string) ([]string, error) {
	var args []string
	var current strings.Builder
	var quote byte
	escaped, inToken := false, false
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case escaped:
			current.WriteByte(c)
			escaped, inToken = false, true
		case c == '\\':
			escaped, inToken = true, true
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				current.WriteByte(c)
			}
			inToken = true
		case c == '\'' || c == '"':
			quote, inToken = c, true
		case c == ' ' || c == '\t' || c == '\n':
			if inToken {
				args = append(args, current.String())
				current.Reset()
				inToken = false
			}
		default:
			current.WriteByte(c)
			inToken = true
		}
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unterminated quote or escape")
	}
	if inToken {
		args = append(args, current.String())
	}
	return args, nil
}
