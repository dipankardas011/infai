package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/dipankardas011/infai/config"
)

const defaultBaseURL = "https://huggingface.co"

var defaultUserAgent = "infai/" + config.Version()

var tokenEnvVars = []string{"HF_TOKEN", "HUGGING_FACE_HUB_TOKEN"}

func TokenFromEnv() string {
	for _, k := range tokenEnvVars {
		if v := os.Getenv(k); v != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	userAgent  string
}

func NewClient(token string) *Client {
	return &Client{
		baseURL:    defaultBaseURL,
		token:      token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		userAgent:  defaultUserAgent,
	}
}

func (c *Client) SetTimeout(d time.Duration) {
	c.httpClient.Timeout = d
}

func (c *Client) Search(ctx context.Context, params SearchParams) ([]ModelInfo, error) {
	u := c.baseURL + "/api/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, &Error{Op: "search", Kind: KindNetwork, Err: err}
	}

	q := req.URL.Query()
	for k, v := range params.values() {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &Error{Op: "search", Kind: KindNetwork, Err: err}
	}
	defer resp.Body.Close()

	if err := checkResponse(resp); err != nil {
		return nil, &Error{Op: "search", Kind: httpKind(resp.StatusCode), Err: err}
	}

	var models []ModelInfo
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		return nil, &Error{Op: "search", Kind: KindOther, Err: err}
	}
	return models, nil
}

func (c *Client) GetRepo(ctx context.Context, repoID string) (*ModelInfo, error) {
	repoID = strings.TrimSpace(repoID)
	if repoID == "" {
		return nil, &Error{Op: "get repo", Kind: KindOther, Err: fmt.Errorf("empty repo id")}
	}

	u := fmt.Sprintf("%s/api/models/%s", c.baseURL, repoID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, &Error{Op: "get repo", Kind: KindNetwork, Err: err}
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &Error{Op: "get repo", Kind: KindNetwork, Err: err}
	}
	defer resp.Body.Close()

	if err := checkResponse(resp); err != nil {
		return nil, &Error{Op: "get repo " + repoID, Kind: httpKind(resp.StatusCode), Err: err}
	}

	var info ModelInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, &Error{Op: "get repo " + repoID, Kind: KindOther, Err: err}
	}
	return &info, nil
}

func (c *Client) ListFiles(ctx context.Context, repoID, revision string) ([]FileEntry, error) {
	repoID = strings.TrimSpace(repoID)
	revision = strings.TrimSpace(revision)
	if repoID == "" {
		return nil, &Error{Op: "list files", Kind: KindOther, Err: fmt.Errorf("empty repo id")}
	}
	if revision == "" {
		revision = "main"
	}

	u := fmt.Sprintf("%s/api/models/%s/tree/%s", c.baseURL, repoID, url.PathEscape(revision))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, &Error{Op: "list files", Kind: KindNetwork, Err: err}
	}

	q := req.URL.Query()
	q.Set("recursive", "true")
	req.URL.RawQuery = q.Encode()
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &Error{Op: "list files", Kind: KindNetwork, Err: err}
	}
	defer resp.Body.Close()

	if err := checkResponse(resp); err != nil {
		return nil, &Error{Op: "list files " + repoID, Kind: httpKind(resp.StatusCode), Err: err}
	}

	var files []FileEntry
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, &Error{Op: "list files " + repoID, Kind: KindOther, Err: err}
	}
	return files, nil
}

func (c *Client) ResolveRevision(ctx context.Context, repoID, revision string) (string, error) {
	info, err := c.GetRepo(ctx, repoID)
	if err != nil {
		return "", err
	}
	if info.SHA != "" {
		return info.SHA, nil
	}
	return revision, nil
}

func (c *Client) BrowserURL(repoID string, subs ...string) string {
	b := strings.TrimRight(c.baseURL, "/") + "/" + repoID
	if len(subs) > 0 {
		b += "/tree/main/" + strings.Join(subs, "/")
	}
	return b
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func checkResponse(resp *http.Response) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = resp.Status
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return fmt.Errorf("rate limited: %s", msg)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("unauthorized: %s", msg)
	case http.StatusNotFound:
		return fmt.Errorf("not found: %s", msg)
	default:
		return fmt.Errorf("%s", msg)
	}
}

func httpKind(code int) ErrorKind {
	switch code {
	case http.StatusTooManyRequests:
		return KindRateLimited
	case http.StatusUnauthorized, http.StatusForbidden:
		return KindUnauthorized
	case http.StatusNotFound:
		return KindNotFound
	default:
		return KindOther
	}
}
