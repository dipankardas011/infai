package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/glue"
	"github.com/dipankardas011/infai/pkg/agent/models"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

// ErrNoSession is returned by Chat when no session has been created/loaded
// yet.
var ErrNoSession = errors.New("client: no active session; create or load one first")

// RemoteClient attaches the CLI to a running <binary> server. The REPL
// explicitly creates or loads a session and calls SetSession; Chat then reuses
// that session, so the conversation persists server-side.
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

func (c *RemoteClient) SetSession(id uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionID = id
}

func (c *RemoteClient) SessionID() uuid.UUID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

func (c *RemoteClient) Chat(ctx context.Context, prompt string, thinking contracts.InfaiThinkingLevel, onDelta func(kind contracts.DeltaKind, text string), onApproval func(ApprovalUpdate)) (*ChatReply, error) {
	c.mu.Lock()
	sessionID := c.sessionID
	c.mu.Unlock()
	if sessionID == uuid.Nil {
		return nil, ErrNoSession
	}

	payload, err := json.Marshal(struct {
		Prompt   string                       `json:"prompt"`
		Thinking contracts.InfaiThinkingLevel `json:"thinking"`
	}{Prompt: prompt, Thinking: thinking})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/sessions/"+sessionID.String()+"/chat", bytes.NewReader(payload))
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

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("server: status %d: %s", resp.StatusCode, string(body))
	}

	reply, err := c.readStream(resp.Body, onDelta, onApproval)
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return reply, err
}

func (c *RemoteClient) ResolveApproval(ctx context.Context, approval Approval, decision string, reason string) error {
	payload, err := json.Marshal(map[string]string{
		"fingerprint": approval.Fingerprint,
		"decision":    decision,
		"reason":      reason,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/sessions/"+approval.SessionID.String()+"/approvals/"+approval.ID.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("server: status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// readStream consumes the SSE chat stream, delivering deltas to onDelta and
// returning the final reply.
func (c *RemoteClient) readStream(body io.Reader, onDelta func(kind contracts.DeltaKind, text string), onApproval func(ApprovalUpdate)) (*ChatReply, error) {
	dec := models.NewDecoder(body)

	var reply ChatReply
	done := false
	for {
		ev, err := dec.Decode()
		if err == io.EOF {
			if !done {
				return nil, io.ErrUnexpectedEOF
			}
			break
		}
		if err != nil {
			return nil, err
		}

		var sseEv struct {
			Kind             string                `json:"kind"`
			Delta            string                `json:"delta"`
			Done             bool                  `json:"done"`
			Reply            string                `json:"reply"`
			ReasoningContent string                `json:"reasoning_content"`
			Status           string                `json:"status"`
			Error            string                `json:"error"`
			SessionID        uuid.UUID             `json:"session_id"`
			Model            string                `json:"model"`
			Name             string                `json:"name,omitempty"`
			ContextWindow    uint64                `json:"ctx_window"`
			Usage            *contracts.TokenUsage `json:"usage"`
			ContextTokens    uint64                `json:"context_tokens"`
			Pending          *Approval             `json:"pending"`
			Type             string                `json:"type"`
			ID               uuid.UUID             `json:"id"`
			Fingerprint      string                `json:"fingerprint"`
			ToolCall         *contracts.ToolCall   `json:"tool_call"`
			Decision         string                `json:"decision"`
			Reason           string                `json:"reason"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &sseEv); err != nil {
			return nil, err
		}
		if sseEv.Type != "" && onApproval != nil {
			onApproval(ApprovalUpdate{
				Type: sseEv.Type,
				Approval: &Approval{
					ID: sseEv.ID, SessionID: sseEv.SessionID,
					Fingerprint: sseEv.Fingerprint, ToolCall: sseEv.ToolCall,
				},
				Decision: sseEv.Decision, Reason: sseEv.Reason,
			})
		}
		if sseEv.Error != "" {
			return nil, fmt.Errorf("server: %s", sseEv.Error)
		}
		if sseEv.Delta != "" {
			kind := contracts.DeltaContent
			switch sseEv.Kind {
			case "reasoning":
				kind = contracts.DeltaReasoning
			case "status":
				kind = contracts.DeltaStatus
			case "compaction_summary":
				kind = contracts.DeltaCompactionSummary
			case "tool_call":
				kind = contracts.DeltaToolCall
			case "tool_result":
				kind = contracts.DeltaToolResult
			case "skill_load":
				kind = contracts.DeltaSkillLoad
			case "task_checklist":
				kind = contracts.DeltaTaskChecklist
			}
			if kind == contracts.DeltaContent {
				reply.Reply += sseEv.Delta
			} else if kind == contracts.DeltaReasoning {
				reply.ReasoningContent += sseEv.Delta
			}
			if onDelta != nil {
				onDelta(kind, sseEv.Delta)
			}
		}
		if sseEv.Done {
			done = true
			reply.Reply = sseEv.Reply
			reply.ReasoningContent = sseEv.ReasoningContent
			reply.Status = sseEv.Status
			reply.SessionID = sseEv.SessionID
			reply.Model = sseEv.Model
			reply.ContextWindow = sseEv.ContextWindow
			reply.Name = sseEv.Name
			reply.Usage = sseEv.Usage
			reply.ContextTokens = sseEv.ContextTokens
			reply.Pending = sseEv.Pending
		}
	}

	return &reply, nil
}

// ---- providers ----

func (c *RemoteClient) ListAllProviderModels(ctx context.Context) ([]glue.ListModelOutput, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}
	var out []glue.ListModelOutput
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *RemoteClient) ProviderAuthMethods(ctx context.Context, providerID contracts.ProviderSlug) ([]contracts.ProviderAuthMethod, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/providers/"+string(providerID)+"/auth-methods", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}
	var methods []contracts.ProviderAuthMethod
	if err := json.NewDecoder(resp.Body).Decode(&methods); err != nil {
		return nil, err
	}
	return methods, nil
}

func (c *RemoteClient) LoginProvider(ctx context.Context, input glue.LoginProviderInput, onUpdate func(glue.LoginProviderOutput)) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/providers/login", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readAPIError(resp)
	}

	dec := models.NewDecoder(resp.Body)
	for {
		event, err := dec.Decode()
		if err == io.EOF {
			return io.ErrUnexpectedEOF
		}
		if err != nil {
			return err
		}
		var output glue.LoginProviderOutput
		if err := json.Unmarshal([]byte(event.Data), &output); err != nil {
			return err
		}
		if onUpdate != nil {
			onUpdate(output)
		}
		switch output.Status {
		case contracts.ProviderAuthComplete:
			return nil
		case contracts.ProviderAuthFailed:
			if output.Error == "" {
				output.Error = "authentication failed"
			}
			return errors.New(output.Error)
		case contracts.ProviderAuthPending:
		default:
			return fmt.Errorf("provider authentication returned unknown status %q", output.Status)
		}
	}
}

func (c *RemoteClient) LogoutProvider(ctx context.Context, providerID contracts.ProviderSlug) error {
	return c.providerAction(ctx, "/v1/providers/logout", glue.LogoutProviderInput{ProviderID: providerID})
}

func (c *RemoteClient) providerAction(ctx context.Context, path string, input any) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return readAPIError(resp)
	}
	return nil
}

// ---- sessions ----

func (c *RemoteClient) CreateSession(ctx context.Context, opts SessionCreateOptions) (*glue.SessionOutput, error) {
	var out glue.SessionOutput
	err := c.postJSONInto(ctx, "/v1/sessions", opts, http.StatusCreated, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *RemoteClient) LoadSession(ctx context.Context, id uuid.UUID) (*glue.SessionOutput, error) {
	var out glue.SessionOutput
	err := c.postJSONInto(ctx, "/v1/sessions/"+id.String()+"/load", struct{}{}, http.StatusOK, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetSession fetches a session's meta and active timeline records so a resumed
// session can render its history.
func (c *RemoteClient) GetSession(ctx context.Context, id uuid.UUID) (*store.SessionMeta, []store.Record, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/sessions/"+id.String(), nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, readAPIError(resp)
	}
	var out struct {
		Meta    store.SessionMeta `json:"meta"`
		Records []store.Record    `json:"records"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, nil, err
	}
	return &out.Meta, out.Records, nil
}

func (c *RemoteClient) GetTimeline(ctx context.Context, id uuid.UUID) (*TimelineView, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/sessions/"+id.String()+"/timeline", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}
	var out TimelineView
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *RemoteClient) SelectBranch(ctx context.Context, id, eventID uuid.UUID) (contracts.TaskChecklistState, error) {
	var response struct {
		TaskChecklist contracts.TaskChecklistState `json:"task_checklist"`
	}
	err := c.postJSONInto(ctx, "/v1/sessions/"+id.String()+"/timeline/branch", map[string]uuid.UUID{"event_id": eventID}, http.StatusOK, &response)
	return response.TaskChecklist, err
}

func (c *RemoteClient) DeleteSession(ctx context.Context, id uuid.UUID) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/v1/sessions/"+id.String(), nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return readAPIError(resp)
	}
	return nil
}

func (c *RemoteClient) RenameSession(ctx context.Context, id uuid.UUID, name string) (*store.SessionMeta, error) {
	var meta store.SessionMeta
	err := c.postJSONInto(ctx, "/v1/sessions/"+id.String()+"/rename", map[string]string{"name": name}, http.StatusOK, &meta)
	if err != nil {
		return nil, err
	}
	return &meta, nil
}

func (c *RemoteClient) ListSessions(ctx context.Context) ([]contracts.SessionSummary, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/sessions", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}
	var out []contracts.SessionSummary
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *RemoteClient) SetSessionModel(ctx context.Context, provider, model string) (*glue.SessionOutput, error) {
	var out glue.SessionOutput
	err := c.postJSONInto(ctx, "/v1/sessions/"+c.SessionID().String()+"/model", map[string]string{"provider": provider, "model": model}, http.StatusOK, &out)
	return &out, err
}

func (c *RemoteClient) Compact(ctx context.Context) (*store.SessionMeta, error) {
	id := c.SessionID()
	if id == uuid.Nil {
		return nil, ErrNoSession
	}
	var meta store.SessionMeta
	if err := c.postJSONInto(ctx, "/v1/sessions/"+id.String()+"/compact", struct{}{}, http.StatusOK, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func (c *RemoteClient) postJSON(ctx context.Context, path string, body any, wantCode int) error {
	var out any
	return c.postJSONInto(ctx, path, body, wantCode, &out)
}

func (c *RemoteClient) postJSONInto(ctx context.Context, path string, body any, wantCode int, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantCode {
		return readAPIError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func readAPIError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return fmt.Errorf("server: %s", e.Error)
	}
	return fmt.Errorf("server: status %d: %s", resp.StatusCode, string(body))
}
