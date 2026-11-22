package engine

import (
	"github.com/dipankardas011/infai/pkg/agent/browser"
	"github.com/google/uuid"
)

func (e *InfaiAgentEngine) Browser() *browser.Bridge { return e.browser }

func (e *InfaiAgentEngine) BrowserToken() string { return e.browserToken }

// sessionBrowser hands one session its browser surface. The bridge stays the
// engine's; a session can only act as itself.
func (e *InfaiAgentEngine) sessionBrowser(id uuid.UUID) *browser.Session {
	if e.browser == nil {
		return nil
	}
	return e.browser.Session(id)
}
