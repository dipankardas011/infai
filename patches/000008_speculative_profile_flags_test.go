package patches

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestM0008MigratesRecognizedSpeculativeFlags(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE inference_engine (id TEXT PRIMARY KEY, kind TEXT);
		CREATE TABLE model_registry (id INTEGER PRIMARY KEY, model_dir TEXT, primary_file TEXT);
		CREATE TABLE profiles (
			id INTEGER PRIMARY KEY, inference_engine_id TEXT, extra_flags TEXT NOT NULL DEFAULT '',
			speculative_mode TEXT NOT NULL DEFAULT '', draft_model_id INTEGER, speculative_tokens INTEGER
		);
		INSERT INTO inference_engine VALUES ('llama', 'llamacpp'), ('vllm', 'vllm');
		INSERT INTO model_registry VALUES
			(1, '/models/target', 'target.gguf'),
			(2, '/models/draft', 'draft.gguf'),
			(3, '/models/vllm-draft', '');
		INSERT INTO profiles VALUES
			(1, 'llama', '--threads 8 --spec-type draft-mtp --spec-draft-n-max 2', '', NULL, NULL),
			(2, 'llama', '--spec-type=draft-mtp --spec-draft-model "/models/draft/draft.gguf" --spec-draft-n-max=4', '', NULL, NULL),
			(3, 'llama', '--spec-type draft-simple --spec-draft-model /models/draft/draft.gguf', '', NULL, NULL),
			(4, 'vllm', '--enforce-eager --speculative-config ''{"method":"mtp","num_speculative_tokens":1}''', '', NULL, NULL),
			(5, 'vllm', '--speculative-config ''{"method":"draft_model","model":"/models/vllm-draft","num_speculative_tokens":3}''', '', NULL, NULL),
			(6, 'llama', '--spec-type draft-mtp --spec-draft-model /missing/mtp.gguf', '', NULL, NULL);`); err != nil {
		t.Fatal(err)
	}

	if err := Apply(db, 7, 8); err != nil {
		t.Fatal(err)
	}
	if err := Apply(db, 7, 8); err != nil {
		t.Fatalf("patch must be idempotent: %v", err)
	}

	tests := []struct {
		id      int
		mode    string
		draftID *int64
		tokens  *int
		extra   string
	}{
		{id: 1, mode: "native_mtp", tokens: intPtr(2), extra: "--threads 8"},
		{id: 2, mode: "mtp_assistant", draftID: int64Ptr(2), tokens: intPtr(4)},
		{id: 3, mode: "draft_model", draftID: int64Ptr(2), tokens: intPtr(3)},
		{id: 4, mode: "native_mtp", tokens: intPtr(1), extra: "--enforce-eager"},
		{id: 5, mode: "draft_model", draftID: int64Ptr(3), tokens: intPtr(3)},
		{id: 6, extra: "--spec-type draft-mtp --spec-draft-model /missing/mtp.gguf"},
	}
	for _, test := range tests {
		var mode, extra string
		var draftID *int64
		var tokens *int
		if err := db.QueryRow(`SELECT speculative_mode, draft_model_id, speculative_tokens, extra_flags FROM profiles WHERE id=?`, test.id).Scan(&mode, &draftID, &tokens, &extra); err != nil {
			t.Fatal(err)
		}
		if mode != test.mode || !equalInt64Ptr(draftID, test.draftID) || !equalIntPtr(tokens, test.tokens) || extra != test.extra {
			t.Errorf("profile %d = mode %q draft %v tokens %v extra %q", test.id, mode, draftID, tokens, extra)
		}
	}
}

func intPtr(value int) *int       { return &value }
func int64Ptr(value int64) *int64 { return &value }

func equalIntPtr(a, b *int) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func equalInt64Ptr(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
