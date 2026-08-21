package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/models"
	"github.com/google/uuid"
)

// RemoteClient attaches the TUI to a running <binary> server. The first Chat
// creates a session; subsequent Chats reuse it, so the conversation persists
// server-side and survives the client. It always streams via SSE.
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

func (c *RemoteClient) Chat(ctx context.Context, prompt string, onDelta func(kind contracts.DeltaKind, text string)) (*ChatReply, error) {
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
	req.URL.RawQuery = "stream=true"
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return c.readStream(resp.Body, onDelta)
}

// readStream consumes the SSE chat stream, delivering deltas to onDelta and
// returning the final reply.
func (c *RemoteClient) readStream(body io.Reader, onDelta func(kind contracts.DeltaKind, text string)) (*ChatReply, error) {
	dec := models.NewDecoder(body)

	var reply ChatReply
	for {
		ev, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		var sseEv struct {
			Kind             string `json:"kind"`
			Delta            string `json:"delta"`
			Done             bool   `json:"done"`
			Reply            string `json:"reply"`
			ReasoningContent string `json:"reasoning_content"`
			Error            string `json:"error"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &sseEv); err != nil {
			return nil, err
		}
		if sseEv.Error != "" {
			return nil, fmt.Errorf("server: %s", sseEv.Error)
		}
		if sseEv.Delta != "" {
			kind := contracts.DeltaContent
			if sseEv.Kind == "reasoning" {
				kind = contracts.DeltaReasoning
			}
			if kind == contracts.DeltaContent {
				reply.Reply += sseEv.Delta
			} else {
				reply.ReasoningContent += sseEv.Delta
			}
			if onDelta != nil {
				onDelta(kind, sseEv.Delta)
			}
		}
		if sseEv.Done {
			reply.Reply = sseEv.Reply
			reply.ReasoningContent = sseEv.ReasoningContent
		}
	}

	return &reply, nil
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
