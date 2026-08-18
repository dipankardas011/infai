package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c := NewClient("")
	c.baseURL = srv.URL
	t.Cleanup(srv.Close)
	return srv, c
}

func TestSearchFiltersGGUF(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") != "gguf" {
			t.Errorf("expected filter=gguf, got %q", r.URL.Query().Get("filter"))
		}
		json.NewEncoder(w).Encode([]ModelInfo{
			{ID: "org/gguf-model", Tags: []string{"gguf"}},
		})
	})

	results, err := c.Search(context.Background(), SearchParams{Filter: "gguf"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "org/gguf-model" {
		t.Fatalf("expected org/gguf-model, got %s", results[0].ID)
	}
}

func TestSearchFiltersSafetensors(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") != "safetensors" {
			t.Errorf("expected filter=safetensors, got %q", r.URL.Query().Get("filter"))
		}
		json.NewEncoder(w).Encode([]ModelInfo{
			{ID: "org/safetensors-model", Tags: []string{"safetensors", "pytorch"}},
		})
	})

	results, err := c.Search(context.Background(), SearchParams{Filter: "safetensors"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "org/safetensors-model" {
		t.Fatalf("expected org/safetensors-model, got %s", results[0].ID)
	}
}

func TestSearchPaginationLimit(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "3" {
			t.Errorf("expected limit=3, got %q", r.URL.Query().Get("limit"))
		}
		json.NewEncoder(w).Encode(make([]ModelInfo, 3))
	})

	results, err := c.Search(context.Background(), SearchParams{Limit: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
}

func TestSearchWithQueryAndSort(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("search") != "llama" {
			t.Errorf("expected search=llama, got %q", q.Get("search"))
		}
		if q.Get("sort") != "downloads" {
			t.Errorf("expected sort=downloads, got %q", q.Get("sort"))
		}
		if q.Get("direction") != "-1" {
			t.Errorf("expected direction=-1, got %q", q.Get("direction"))
		}
		json.NewEncoder(w).Encode([]ModelInfo{{ID: "meta-llama/Llama-3.2-1B"}})
	})

	results, err := c.Search(context.Background(), SearchParams{
		Query: "llama", Sort: "downloads", Direction: "-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestSearchFullInfo(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("full") != "true" {
			t.Errorf("expected full=true, got %q", r.URL.Query().Get("full"))
		}
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{"id": "org/model", "sha": "abc123", "siblings": []map[string]interface{}{
				{"rfilename": "model.gguf", "size": 1000},
			}},
		})
	})

	results, err := c.Search(context.Background(), SearchParams{Full: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results[0].Siblings) != 1 {
		t.Fatalf("expected siblings in full search")
	}
}

func TestGetRepoFullInfo(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "meta-llama/Llama-3.2-1B") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":           "meta-llama/Llama-3.2-1B",
			"author":       "meta-llama",
			"sha":          "abc123def456",
			"downloads":    500000,
			"likes":        120,
			"lastModified": "2025-01-15T10:30:00.000Z",
			"pipeline_tag": "text-generation",
			"tags":         []string{"gguf", "llama"},
			"siblings": []map[string]interface{}{
				{"rfilename": "model-q4_k_m.gguf", "size": 1234567, "lfs": map[string]interface{}{"sha256": "fedcba", "pointerSize": 135}},
				{"rfilename": "tokenizer.model", "size": 500},
			},
		})
	})

	info, err := c.GetRepo(context.Background(), "meta-llama/Llama-3.2-1B")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.SHA != "abc123def456" {
		t.Fatalf("expected SHA abc123def456, got %s", info.SHA)
	}
	if info.Author != "meta-llama" {
		t.Fatalf("expected author meta-llama, got %s", info.Author)
	}
	if len(info.Siblings) != 2 {
		t.Fatalf("expected 2 siblings, got %d", len(info.Siblings))
	}
	if info.Siblings[0].LFS == nil || info.Siblings[0].LFS.SHA256 != "fedcba" {
		t.Fatalf("expected LFS info, got %#v", info.Siblings[0].LFS)
	}
	if info.PipelineTag != "text-generation" {
		t.Fatalf("expected pipeline_tag text-generation, got %s", info.PipelineTag)
	}
}

func TestListFilesRecursive(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("recursive") != "true" {
			t.Errorf("expected recursive=true, got %q", r.URL.Query().Get("recursive"))
		}
		if !strings.Contains(r.URL.Path, "tree/main") {
			t.Errorf("expected tree/main in path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{"path": "model.gguf", "size": 5000000, "lfs": map[string]interface{}{"sha256": "abc", "pointerSize": 131}},
			{"path": "subdir/tokenizer.json", "size": 2048},
		})
	})

	files, err := c.ListFiles(context.Background(), "org/repo", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].Path != "model.gguf" {
		t.Fatalf("expected model.gguf first, got %s", files[0].Path)
	}
	if files[0].LFS == nil || files[0].LFS.SHA256 != "abc" {
		t.Fatalf("expected LFS sha256=abc, got %#v", files[0].LFS)
	}
	if files[1].Path != "subdir/tokenizer.json" {
		t.Fatalf("expected subdir/tokenizer.json, got %s", files[1].Path)
	}
}

func TestListFilesDefaultRevision(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "tree/main") {
			t.Errorf("expected tree/main in path when revision empty: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode([]FileEntry{})
	})

	_, err := c.ListFiles(context.Background(), "org/repo", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveRevision(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ModelInfo{ID: "org/repo", SHA: "def456abc"})
	})

	sha, err := c.ResolveRevision(context.Background(), "org/repo", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "def456abc" {
		t.Fatalf("expected def456abc, got %s", sha)
	}
}

func TestBrowserURL(t *testing.T) {
	c := NewClient("")

	u := c.BrowserURL("meta-llama/Llama-3.2-1B")
	if u != "https://huggingface.co/meta-llama/Llama-3.2-1B" {
		t.Fatalf("unexpected URL: %s", u)
	}

	u = c.BrowserURL("org/repo", "model.gguf")
	if u != "https://huggingface.co/org/repo/tree/main/model.gguf" {
		t.Fatalf("unexpected URL with sub: %s", u)
	}
}

func TestRateLimitError(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("rate limit exceeded"))
	})

	_, err := c.Search(context.Background(), SearchParams{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsRateLimited(err) {
		t.Fatalf("expected rate limited error, got: %v", err)
	}
}

func TestUnauthorizedError(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("invalid token"))
	})

	_, err := c.GetRepo(context.Background(), "org/private-model")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsUnauthorized(err) {
		t.Fatalf("expected unauthorized error, got: %v", err)
	}
}

func TestForbiddenError(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("access denied"))
	})

	_, err := c.GetRepo(context.Background(), "org/gated-model")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsUnauthorized(err) {
		t.Fatalf("expected unauthorized (403) error, got: %v", err)
	}
}

func TestNotFoundError(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("repo not found"))
	})

	_, err := c.GetRepo(context.Background(), "org/nonexistent")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsNotFound(err) {
		t.Fatalf("expected not found error, got: %v", err)
	}
}

func TestContextCancellation(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Search(ctx, SearchParams{})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("expected canceled error, got: %v", err)
	}
}

func TestContextDeadlineExceeded(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
	})

	c.httpClient.Timeout = 10 * time.Millisecond

	_, err := c.GetRepo(context.Background(), "org/repo")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "Timeout") && !strings.Contains(err.Error(), "Deadline") && !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("expected timeout-related error, got: %v", err)
	}
}

func TestAuthTokenSent(t *testing.T) {
	srv, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer hf_test123" {
			t.Errorf("expected Authorization header, got %q", auth)
		}
		json.NewEncoder(w).Encode([]ModelInfo{})
	})
	c.token = "hf_test123"
	_ = srv

	_, err := c.Search(context.Background(), SearchParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTokenNotInError(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("invalid credentials"))
	})
	c.token = "hf_secret_token_abc"

	_, err := c.GetRepo(context.Background(), "org/repo")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "hf_secret_token_abc") {
		t.Fatal("token leaked in error message")
	}
	if strings.Contains(err.Error(), "Bearer") {
		t.Fatal("authorization header leaked in error message")
	}
}

func TestUserAgentSet(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "infai/") {
			t.Errorf("expected infai/ user agent, got %q", r.Header.Get("User-Agent"))
		}
		json.NewEncoder(w).Encode([]ModelInfo{{ID: "test"}})
	})

	_, err := c.Search(context.Background(), SearchParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEmptyRepoID(t *testing.T) {
	c := NewClient("")

	_, err := c.GetRepo(context.Background(), "  ")
	if err == nil {
		t.Fatal("expected error for empty repo id")
	}

	_, err = c.ListFiles(context.Background(), "", "main")
	if err == nil {
		t.Fatal("expected error for empty repo id")
	}
}

func TestTokenFromEnv(t *testing.T) {
	t.Setenv("HF_TOKEN", "env-token-value")
	if tok := TokenFromEnv(); tok != "env-token-value" {
		t.Fatalf("expected env-token-value, got %s", tok)
	}

	t.Setenv("HF_TOKEN", "")
	t.Setenv("HUGGING_FACE_HUB_TOKEN", "hub-token-value")
	if tok := TokenFromEnv(); tok != "hub-token-value" {
		t.Fatalf("expected hub-token-value, got %s", tok)
	}
}

func TestGatedStatusUnmarshal(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		isGated bool
	}{
		{"bool false", `{"gated":false}`, false},
		{"bool true", `{"gated":true}`, true},
		{"string manual", `{"gated":"manual"}`, true},
		{"string auto", `{"gated":"auto"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var info ModelInfo
			if err := json.Unmarshal([]byte(tt.json), &info); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if info.Gated.IsGated != tt.isGated {
				t.Fatalf("expected gated=%v, got %v", tt.isGated, info.Gated.IsGated)
			}
		})
	}
}

func TestSearchEmptyResults(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]ModelInfo{})
	})

	results, err := c.Search(context.Background(), SearchParams{Query: "nonexistent-model-xyz"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestErrorWrapping(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})

	_, err := c.Search(context.Background(), SearchParams{})
	if err == nil {
		t.Fatal("expected error")
	}

	var hubErr *Error
	if !errors.As(err, &hubErr) || hubErr.Kind != KindRateLimited {
		t.Fatalf("expected KindRateLimited in error chain: %v", err)
	}
}

func TestListFilesOnGatedRepo(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("gated repository"))
	})

	_, err := c.ListFiles(context.Background(), "org/gated-repo", "main")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsUnauthorized(err) {
		t.Fatalf("expected unauthorized for gated repo, got: %v", err)
	}
}

func TestListFilesNonexistentRevision(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "nonexistent-revision") {
			t.Errorf("expected nonexistent-revision in path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	})

	_, err := c.ListFiles(context.Background(), "org/repo", "nonexistent-revision")
	if err == nil {
		t.Fatal("expected error for nonexistent revision")
	}
	if !IsNotFound(err) {
		t.Fatalf("expected not found error, got: %v", err)
	}
}

func TestSetTimeout(t *testing.T) {
	c := NewClient("")
	c.SetTimeout(5 * time.Second)
	if c.httpClient.Timeout != 5*time.Second {
		t.Fatalf("expected 5s timeout, got %v", c.httpClient.Timeout)
	}
}

func TestSearchAllParams(t *testing.T) {
	srv, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("search") != "mistral" {
			t.Errorf("search: %s", q.Get("search"))
		}
		if q.Get("filter") != "gguf" {
			t.Errorf("filter: %s", q.Get("filter"))
		}
		if q.Get("sort") != "lastModified" {
			t.Errorf("sort: %s", q.Get("sort"))
		}
		if q.Get("direction") != "-1" {
			t.Errorf("direction: %s", q.Get("direction"))
		}
		if q.Get("limit") != "10" {
			t.Errorf("limit: %s", q.Get("limit"))
		}
		if q.Get("full") != "true" {
			t.Errorf("full: %s", q.Get("full"))
		}
		json.NewEncoder(w).Encode([]ModelInfo{})
	})
	_ = srv

	_, err := c.Search(context.Background(), SearchParams{
		Query: "mistral", Filter: "gguf", Sort: "lastModified",
		Direction: "-1", Limit: 10, Full: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveRevisionFallback(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ModelInfo{ID: "org/repo"})
	})

	sha, err := c.ResolveRevision(context.Background(), "org/repo", "my-branch")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "my-branch" {
		t.Fatalf("expected fallback to my-branch, got %s", sha)
	}
}

func TestGatedStatusStringReason(t *testing.T) {
	var info ModelInfo
	if err := json.Unmarshal([]byte(`{"gated":"manual"}`), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !info.Gated.IsGated {
		t.Fatal("expected gated=true")
	}
	if info.Gated.Reason != "manual" {
		t.Fatalf("expected reason=manual, got %s", info.Gated.Reason)
	}
}

func TestSearchParamsNilValuesOmitted(t *testing.T) {
	srv, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Has("search") {
			t.Error("search should not be present")
		}
		if q.Has("filter") {
			t.Error("filter should not be present")
		}
		if q.Has("sort") {
			t.Error("sort should not be present")
		}
		json.NewEncoder(w).Encode([]ModelInfo{})
	})
	_ = srv

	_, err := c.Search(context.Background(), SearchParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestServerErrorResponse(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	})

	_, err := c.Search(context.Background(), SearchParams{})
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if !strings.Contains(err.Error(), "internal error") {
		t.Fatalf("expected error body, got: %v", err)
	}
}
