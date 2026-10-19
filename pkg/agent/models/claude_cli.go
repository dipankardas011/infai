package models

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	harnessErr "github.com/dipankardas011/infai/pkg/agent/errors"
)

// claude-cli authenticates outside infai: the user signs the locally installed
// Claude Code CLI in themselves. infai never reads, stores or forwards a
// credential for this provider, so the only supported method is "none".
//
// Adding the provider is still where the CLI is checked. A provider that could
// never answer is worth rejecting at add time rather than at the first prompt,
// so the flow refuses to complete unless the CLI is on PATH and already signed
// in. It reports only that; the CLI's own status output also names an account
// and an organisation, and infai keeps none of it.
func claudeCLIAuthMethods() []contracts.ProviderAuthMethod {
	return []contracts.ProviderAuthMethod{{
		Method:      contracts.NoneAuth,
		Name:        "Local Claude Code login",
		Description: "Uses the Claude Code CLI already signed in on this machine",
	}}
}

func beginClaudeCLIAuth(ctx context.Context, method contracts.LLMProviderAuthMethod) (contracts.ProviderAuthFlow, error) {
	if method != contracts.NoneAuth {
		return nil, fmt.Errorf("claude cli does not support auth method %q", method)
	}
	if err := checkClaudeCLIAvailable(ctx, "claude"); err != nil {
		return nil, err
	}
	return staticAuthFlow{auth: contracts.LLMProviderAuth{Method: contracts.NoneAuth}}, nil
}

// claudeCLI runs the user's own, unmodified Claude Code CLI once per model
// turn. infai owns the agent loop, the history, tool execution and approvals;
// the CLI answers inside a JSON Schema infai supplies, so tool requests come
// back as structured data.
//
// The CLI keeps its own copy of the conversation. Each turn resumes that copy
// and appends only the new entries, which is what keeps the prompt prefix
// identical and lets the CLI's message-level cache breakpoint hit.
type claudeCLI struct {
	model  contracts.ProvisionedModel
	binary string
	// opts override the defaults in claudeCLIOptions; tests set them.
	opts claudeCLIOptions
	// conversation is the CLI session this adaptor appends to. An adaptor is
	// created per infai session, so the state has exactly one owner.
	conversation claudeResumeState
}

type claudeCLIOptions struct {
	// workspace is the session's working directory. The CLI reports its
	// process cwd to the model in its environment block, so running it here
	// keeps that report in sync with infai's system prompt. --system-prompt
	// keeps CLAUDE.md and git status out of the request, so the real
	// directory is safe.
	workspace  string
	retryDelay time.Duration
	// memoryMax and pidsMax bound each CLI turn when the unit delegated a
	// cgroup subtree to this process. Zero means no limit of that kind.
	memoryMax uint64
	pidsMax   int
}

func NewClaudeCLI(model contracts.ProvisionedModel, workspace string, sessionID uuid.UUID) (*claudeCLI, error) {
	binary := strings.TrimSpace(model.BaseEndpoint())
	if binary == "" {
		binary = "claude"
	}
	client := &claudeCLI{model: model, binary: binary, opts: claudeCLIOptions{
		workspace: workspace,
		memoryMax: claudeCLIMemoryMax,
		pidsMax:   claudeCLIPidsMax,
	}}
	client.conversation.id = sessionID.String()
	return client, nil
}

func (c *claudeCLI) GetModelSpecs() contracts.ProvisionedModel { return c.model }

// resolveBinary locates the CLI, sharing its rule with the add-time preflight.
func (c *claudeCLI) resolveBinary() (string, error) {
	return resolveClaudeBinary(c.binary)
}

func (c *claudeCLI) attempts(ctx context.Context, systemPrompt string, turn claudeTurn, schema string, allowed claudeTools, opts *contracts.GenerateOptions) (claudeStreamResult, error) {
	var last claudeStreamResult
	var err error
	for attempt := 1; attempt <= claudeMaxAttempts; attempt++ {
		last, err = c.run(ctx, systemPrompt, turn, attempt, schema, allowed, opts)
		if err == nil || !last.Retryable || attempt == claudeMaxAttempts {
			return last, err
		}
		if opts != nil && opts.OnDelta != nil {
			notice := fmt.Sprintf("Claude is retrying (attempt %d/%d)", attempt+1, claudeMaxAttempts)
			if opts.OnDelta(contracts.EventProviderEvent, notice) {
				return last, harnessErr.ErrTurnCanceled
			}
		}
		timer := time.NewTimer(c.retryDelay())
		select {
		case <-ctx.Done():
			timer.Stop()
			return last, ctx.Err()
		case <-timer.C:
		}
	}
	return last, err
}

func (c *claudeCLI) retryDelay() time.Duration {
	if c.opts.retryDelay > 0 {
		return c.opts.retryDelay
	}
	return 5 * time.Second
}

func (c *claudeCLI) Generate(ctx context.Context, messages []contracts.ChatMessage, tools []contracts.Tool, opts *contracts.GenerateOptions) (contracts.ChatMessage, *contracts.TokenUsage, error) {
	schema, allowed, err := claudeToolSchema(tools)
	if err != nil {
		return contracts.ChatMessage{}, nil, err
	}
	systemPrompt := claudeSystemPrompt(messages)
	history := claudeConversation(messages)

	// The session state and the CLI turn are one critical section: a second
	// Generate (compaction) must not append to or replace the same CLI
	// conversation while this one is in flight.
	c.conversation.mu.Lock()
	defer c.conversation.mu.Unlock()

	turn := c.planTurn(history, systemPrompt)
	result, err := c.attempts(ctx, systemPrompt, turn, schema, allowed, opts)
	if err != nil {
		c.recordFailure()
		return contracts.ChatMessage{}, result.Usage, err
	}
	reply, err := claudeMessage(result, allowed)
	if err != nil {
		c.recordFailure()
		return contracts.ChatMessage{}, result.Usage, err
	}
	if result.NativeTool != nil {
		// infai terminated the process before the CLI could finish the turn,
		// so its conversation now ends on a tool_use with no tool_result.
		c.recordFailure()
	} else {
		// The CLI stored this turn. The next delta skips the assistant reply,
		// which the CLI already holds, and carries the tool results.
		c.recordSuccess(result.SessionID, history, systemPrompt, reply)
	}
	if opts != nil && opts.OnDelta != nil {
		// The validated object only exists once the turn is complete, so the
		// visible answer is published in one piece rather than token by token.
		//
		// Tool calls are deliberately not published here: the agent loop
		// publishes EventToolCall for every call in the reply it commits, and
		// publishing in both places renders each call twice in the live view.
		if reply.Content != nil && *reply.Content != "" {
			if opts.OnDelta(contracts.DeltaContent, *reply.Content) {
				// The caller will not commit this reply, so the stored CLI
				// conversation no longer lines up with infai's history.
				c.recordFailure()
				return contracts.ChatMessage{}, result.Usage, harnessErr.ErrTurnCanceled
			}
		}
	}
	return reply, result.Usage, nil
}
