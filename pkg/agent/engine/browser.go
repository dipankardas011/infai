package engine

import "github.com/dipankardas011/infai/pkg/agent/browser"

func (e *InfaiAgentEngine) Browser() *browser.Bridge { return e.browser }

func (e *InfaiAgentEngine) BrowserToken() string { return e.browserToken }

func (e *InfaiAgentEngine) sessionBrowser() browser.Driver {
	if e.browser == nil {
		return nil
	}
	return e.browser
}
