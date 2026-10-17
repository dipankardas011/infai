package models

import (
	"context"
	"fmt"
	"strings"
	"time"

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
// the CLI is used only as a stateless completion engine that answers inside a
// JSON Schema infai supplies, so tool requests come back as structured data.
type claudeCLI struct {
	model  contracts.ProvisionedModel
	binary string
	// opts override the defaults in claudeCLIOptions; tests set them.
	opts claudeCLIOptions
}

type claudeCLIOptions struct {
	// workDir is the empty, infai-owned directory the CLI runs in. It must
	// never be the user's repository: the CLI would otherwise pull CLAUDE.md
	// and git status into the prompt.
	workDir    string
	minVersion string
	retryDelay time.Duration
	// memoryMax and pidsMax bound each CLI turn when the unit delegated a
	// cgroup subtree to this process. Zero means no limit of that kind.
	memoryMax uint64
	pidsMax   int
}

func NewClaudeCLI(model contracts.ProvisionedModel) (*claudeCLI, error) {
	binary := strings.TrimSpace(model.BaseEndpoint())
	if binary == "" {
		binary = "claude"
	}
	workDir, err := claudeCLIWorkDir()
	if err != nil {
		return nil, err
	}
	return &claudeCLI{model: model, binary: binary, opts: claudeCLIOptions{
		workDir:    workDir,
		minVersion: claudeCLIMinVersion,
		memoryMax:  claudeCLIMemoryMax,
		pidsMax:    claudeCLIPidsMax,
	}}, nil
}

func (c *claudeCLI) GetModelSpecs() contracts.ProvisionedModel { return c.model }

// resolveBinary locates the CLI, sharing its rule with the add-time preflight.
func (c *claudeCLI) resolveBinary() (string, error) {
	return resolveClaudeBinary(c.binary)
}

func (c *claudeCLI) attempts(ctx context.Context, systemPrompt, transcript, schema string, allowed claudeTools, opts *contracts.GenerateOptions) (claudeStreamResult, error) {
	var last claudeStreamResult
	var err error
	for attempt := 1; attempt <= claudeMaxAttempts; attempt++ {
		last, err = c.run(ctx, systemPrompt, transcript, schema, allowed, opts)
		if err == nil || !last.Retryable || attempt == claudeMaxAttempts {
			return last, err
		}
		if opts != nil && opts.OnDelta != nil {
			notice := fmt.Sprintf("Claude is overloaded; retrying (attempt %d/%d)", attempt+1, claudeMaxAttempts)
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
	result, err := c.attempts(ctx, claudeSystemPrompt(messages), claudeTranscriptText(messages), schema, allowed, opts)
	if err != nil {
		return contracts.ChatMessage{}, result.Usage, err
	}
	reply, err := claudeMessage(result, allowed)
	if err != nil {
		return contracts.ChatMessage{}, result.Usage, err
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
				return contracts.ChatMessage{}, result.Usage, harnessErr.ErrTurnCanceled
			}
		}
	}
	return reply, result.Usage, nil
}
