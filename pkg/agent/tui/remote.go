package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/google/uuid"
)

// RemoteClient attaches the TUI to a running <binary> server. The first Chat
// creates a session; subsequent Chats reuse it, so the conversation persists
// server-side and survives the client.
type RemoteClient struct {
	baseURL   string
	client    *http.Client
	mu        sync.Mutex
	sessionID uuid.UUID
}

func NewRemoteClient(baseURL string) *RemoteClient {
	return &RemoteClient{
		baseURL: baseURL,
		client:  &http.Client{},
	}
}

func (c *RemoteClient) Chat(ctx context.Context, prompt string) (*ChatReply, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sessionID == uuid.Nil {
		id, err := c.createSession(ctx)
		if err != nil {
			return nil, err
		}
		c.sessionID = id
	}

	payload, err := json.Marshal(map[string]string{"prompt": prompt})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/sessions/"+c.sessionID.String()+"/chat", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var res struct {
		SessionID        uuid.UUID `json:"session_id"`
		Status           string    `json:"status"`
		Reply            string    `json:"reply"`
		ReasoningContent string    `json:"reasoning_content"`
		Error            string    `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		if res.Error != "" {
			return nil, fmt.Errorf("server: %s", res.Error)
		}
		return nil, fmt.Errorf("chat failed (HTTP %d)", resp.StatusCode)
	}

	return &ChatReply{Reply: res.Reply, ReasoningContent: res.ReasoningContent}, nil
}
func (c *RemoteClient) createSession(ctx context.Context) (uuid.UUID, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/sessions", nil)
	if err != nil {
		return uuid.Nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return uuid.Nil, err
	}
	defer resp.Body.Close()

	var out struct {
		Id uuid.UUID `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return uuid.Nil, err
	}

	return out.Id, nil
}
