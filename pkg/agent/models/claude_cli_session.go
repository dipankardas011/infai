package models

import (
	"crypto/sha256"
	"sync"

	"github.com/google/uuid"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// claudeTurnInput is one CLI user message: the transcript and the image blocks
// it references.
type claudeTurnInput struct {
	transcript string
	images     []contracts.ImageInput
}

func claudeTurnInputOf(history []contracts.ChatMessage) claudeTurnInput {
	transcript, images := claudeTranscriptParts(history)
	return claudeTurnInput{transcript: transcript, images: images}
}

// claudeTurn is what one Generate call feeds the CLI. A resumed turn carries
// only the delta; full is the transcript used when the conversation is absent,
// diverged, or has to be retried.
type claudeTurn struct {
	resume    bool
	sessionID string
	delta     claudeTurnInput
	full      claudeTurnInput
}

// claudeConversation tracks how much of infai's history the CLI conversation
// already holds.
//
// The CLI stores its own message list and puts its cache breakpoint on the
// last message. Re-sending the whole history as one user message therefore
// rewrites that message into the cache every turn; appending only the new
// entries keeps the prefix identical and turns the same bytes into a cache
// read.
type claudeResumeState struct {
	mu sync.Mutex
	// id names the current CLI conversation. It starts as the infai session
	// UUID, so the two can be correlated; a retry or a diverged history
	// starts a later generation with its own UUID.
	id string
	// sent is the number of non-system messages the CLI list accounts for,
	// including the assistant replies it produced.
	sent int
	// prefix hashes infai's serialization of history[:sent], so a rewritten
	// history (compaction) starts a new CLI session instead of appending to a
	// conversation that no longer matches.
	prefix [32]byte
	// system hashes the system prompt. The CLI records it on the first request
	// and reuses it on resume, so a different prompt needs a new session.
	system [32]byte
	// pending is set when the next infai history entry is the assistant reply
	// the CLI already stored. It is skipped in the next delta.
	pending bool
	// pendingHash identifies that reply. A branch switch can put a different
	// assistant message at the same position; the reply must match before it
	// is skipped, or the model would see the other branch's answer.
	pendingHash [32]byte
	// dirty forbids resume until a fresh session succeeds. It is set whenever
	// the CLI turn did not finish: a native tool call stops the process, and a
	// canceled or failed turn may have appended a partial message.
	dirty bool
}

// planTurn decides whether the next request can append to the stored CLI
// conversation or has to start a new one. The caller holds conversation.mu.
func (c *claudeCLI) planTurn(history []contracts.ChatMessage, systemPrompt string) claudeTurn {
	conversation := &c.conversation
	// A fresh turn uses the infai UUID only while this adaptor has not
	// completed a turn. After that the id may exist on disk, so a new
	// generation needs a new UUID. An in-use id from an earlier process is
	// caught by the CLI's "already in use" error and retried the same way.
	fresh := claudeTurn{full: claudeTurnInputOf(history), sessionID: conversation.id}
	if conversation.sent > 0 || fresh.sessionID == "" {
		fresh.sessionID = uuid.NewString()
	}
	if conversation.dirty || conversation.id == "" ||
		conversation.sent > len(history) ||
		conversation.system != claudeHashText(systemPrompt) ||
		conversation.prefix != claudeHashHistory(history[:conversation.sent]) {
		return fresh
	}

	start := conversation.sent
	if conversation.pending {
		// The CLI stored its own copy of the assistant reply, so infai's copy
		// must not be sent again. A different reply at this position means a
		// branch switch, and the CLI conversation is stale.
		if start >= len(history) || history[start].Role != "assistant" ||
			claudeHashMessage(history[start]) != conversation.pendingHash {
			return fresh
		}
		start++
	}
	if start >= len(history) {
		return fresh
	}

	return claudeTurn{
		resume:    true,
		sessionID: conversation.id,
		delta:     claudeTurnInputOf(history[start:]),
		full:      fresh.full,
	}
}

// recordSuccess remembers the conversation the CLI just appended to. history
// is what this turn sent, so the reply the CLI produced sits at history[len].
// The caller holds conversation.mu.
func (c *claudeCLI) recordSuccess(sessionID string, history []contracts.ChatMessage, systemPrompt string, reply contracts.ChatMessage) {
	conversation := &c.conversation
	conversation.id = sessionID
	conversation.sent = len(history)
	conversation.prefix = claudeHashHistory(history)
	conversation.system = claudeHashText(systemPrompt)
	conversation.pending = true
	conversation.pendingHash = claudeHashMessage(reply)
	conversation.dirty = false
}

// recordFailure makes the next turn start a new CLI session. The caller holds
// conversation.mu.
func (c *claudeCLI) recordFailure() {
	c.conversation.dirty = true
}

func claudeHashText(text string) [32]byte {
	return sha256.Sum256([]byte(text))
}

func claudeHashHistory(history []contracts.ChatMessage) [32]byte {
	text, _ := claudeTranscriptParts(history)
	return claudeHashText(text)
}

func claudeHashMessage(message contracts.ChatMessage) [32]byte {
	text, _ := claudeTranscriptParts([]contracts.ChatMessage{message})
	return claudeHashText(text)
}
