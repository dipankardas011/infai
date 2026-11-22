package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// browserPreviewTextLimit bounds the free text (typed text, element
// descriptions, URLs) a one-line browser preview carries.
const browserPreviewTextLimit = 80

type browserArgs struct {
	URL       string `json:"url"`
	Ref       string `json:"ref"`
	Element   string `json:"element"`
	Text      string `json:"text"`
	Value     string `json:"value"`
	Submit    bool   `json:"submit"`
	MaxChars  int    `json:"max_chars"`
	TimeoutMS int    `json:"timeout_ms"`
}

func isBrowserTool(name string) bool {
	switch contracts.ToolType(name) {
	case contracts.BrowserNavigateTool, contracts.BrowserSnapshotTool, contracts.BrowserReadTool,
		contracts.BrowserWaitForTool, contracts.BrowserClickTool, contracts.BrowserTypeTool,
		contracts.BrowserSelectTool:
		return true
	}
	return false
}

// decodeBrowserArgs reports false for non-browser tools, undecodable arguments,
// or a call missing the field its action cannot do without, so callers fall
// back to the generic rendering.
func decodeBrowserArgs(call contracts.ToolCall) (browserArgs, bool) {
	var args browserArgs
	if !isBrowserTool(string(call.Function.Name)) {
		return args, false
	}
	raw := strings.TrimSpace(call.Function.Arguments)
	if raw == "" {
		raw = "{}"
	}
	if json.Unmarshal([]byte(raw), &args) != nil {
		return args, false
	}
	switch contracts.ToolType(call.Function.Name) {
	case contracts.BrowserNavigateTool:
		return args, args.URL != ""
	case contracts.BrowserWaitForTool:
		return args, args.Text != ""
	case contracts.BrowserClickTool, contracts.BrowserTypeTool, contracts.BrowserSelectTool:
		return args, args.Ref != ""
	}
	return args, true
}

func browserPreviewText(value string) string {
	return truncateLine(singleLine(value), browserPreviewTextLimit)
}

// browserTarget is `e12 · button "Place order"`, or just the ref when the model
// gave no description.
func browserTarget(args browserArgs) string {
	if element := browserPreviewText(args.Element); element != "" {
		return args.Ref + " · " + element
	}
	return args.Ref
}

// browserToolPreview is the one-line preview of a browser tool call, shared by
// the approval band and the transcript.
func browserToolPreview(call contracts.ToolCall) (string, bool) {
	args, ok := decodeBrowserArgs(call)
	if !ok {
		return "", false
	}
	switch contracts.ToolType(call.Function.Name) {
	case contracts.BrowserNavigateTool:
		return "navigate " + browserPreviewText(args.URL), true
	case contracts.BrowserSnapshotTool:
		return "snapshot", true
	case contracts.BrowserReadTool:
		if args.MaxChars > 0 {
			return fmt.Sprintf("read page text (max %d chars)", args.MaxChars), true
		}
		return "read page text", true
	case contracts.BrowserWaitForTool:
		preview := fmt.Sprintf("wait for %q", browserPreviewText(args.Text))
		if args.TimeoutMS > 0 {
			preview += fmt.Sprintf(" (timeout %dms)", args.TimeoutMS)
		}
		return preview, true
	case contracts.BrowserClickTool:
		return "click " + browserTarget(args), true
	case contracts.BrowserTypeTool:
		preview := fmt.Sprintf("type %q into %s", browserPreviewText(args.Text), browserTarget(args))
		if args.Submit {
			preview += " (then Enter)"
		}
		return preview, true
	case contracts.BrowserSelectTool:
		return fmt.Sprintf("select %q in %s", browserPreviewText(args.Value), browserTarget(args)), true
	}
	return "", false
}

// browserApprovalBody is the approval-card body for a browser tool call, in the
// uppercase-label style of the other approval bodies.
func browserApprovalBody(call contracts.ToolCall) (string, bool) {
	args, ok := decodeBrowserArgs(call)
	if !ok {
		return "", false
	}
	element := func() string {
		if args.Element == "" {
			return "ELEMENT  ref " + args.Ref
		}
		return fmt.Sprintf("ELEMENT  %s  (ref %s)", singleLine(args.Element), args.Ref)
	}
	const outlined = "The element is outlined in red in the session's Chrome tab."
	var lines []string
	switch contracts.ToolType(call.Function.Name) {
	case contracts.BrowserNavigateTool:
		lines = []string{"BROWSER ACTION  NAVIGATE", "URL  " + args.URL}
	case contracts.BrowserSnapshotTool:
		lines = []string{"BROWSER ACTION  SNAPSHOT", "Captures the page structure of the session's Chrome tab."}
	case contracts.BrowserReadTool:
		lines = []string{"BROWSER ACTION  READ PAGE TEXT"}
		if args.MaxChars > 0 {
			lines = append(lines, fmt.Sprintf("MAX CHARS  %d", args.MaxChars))
		}
	case contracts.BrowserWaitForTool:
		lines = []string{"BROWSER ACTION  WAIT FOR TEXT", fmt.Sprintf("TEXT  %q", args.Text)}
		if args.TimeoutMS > 0 {
			lines = append(lines, fmt.Sprintf("TIMEOUT  %d ms", args.TimeoutMS))
		}
	case contracts.BrowserClickTool:
		lines = []string{"BROWSER ACTION  CLICK", element(), outlined}
	case contracts.BrowserTypeTool:
		submit := "no"
		if args.Submit {
			submit = "yes (press Enter after typing)"
		}
		lines = []string{"BROWSER ACTION  TYPE", element(), fmt.Sprintf("TEXT  %q", args.Text), "SUBMIT  " + submit, outlined}
	case contracts.BrowserSelectTool:
		lines = []string{"BROWSER ACTION  SELECT", element(), fmt.Sprintf("VALUE  %q", args.Value), outlined}
	default:
		return "", false
	}
	return strings.Join(lines, "\n"), true
}
