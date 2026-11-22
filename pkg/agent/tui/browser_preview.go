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

// browserArgs is the union of the five tools' arguments. Each key matches the
// tool it belongs to, so one decode covers whichever tool was called.
type browserArgs struct {
	URL    string `json:"url"`
	Client string `json:"client"`

	How      string `json:"how"`
	Page     int    `json:"page"`
	MaxChars int    `json:"max_chars"`
	WaitFor  string `json:"wait_for"`

	By      string `json:"by"`
	Action  string `json:"action"`
	Element string `json:"element"`
	Ref     string `json:"ref"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	Text    string `json:"text"`
	Value   string `json:"value"`
	Submit  bool   `json:"submit"`

	To    string `json:"to"`
	Times int    `json:"times"`
}

func isBrowserTool(name string) bool {
	switch contracts.ToolType(name) {
	case contracts.BrowserClientsTool, contracts.BrowserNavigateTool, contracts.BrowserReadTool,
		contracts.BrowserActTool, contracts.BrowserScrollTool:
		return true
	}
	return false
}

// decodeBrowserArgs reports false for non-browser tools, undecodable arguments,
// or a call missing the field its tool cannot do without, so callers fall back
// to the generic rendering.
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
	case contracts.BrowserActTool:
		return args, args.By != ""
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

// browserPoint is `120,340 · button "Place order"`.
func browserPoint(args browserArgs) string {
	point := fmt.Sprintf("%d,%d", args.X, args.Y)
	if element := browserPreviewText(args.Element); element != "" {
		return point + " · " + element
	}
	return point
}

// browserToolPreview is the one-line preview of a browser tool call, shared by
// the approval band and the transcript.
func browserToolPreview(call contracts.ToolCall) (string, bool) {
	args, ok := decodeBrowserArgs(call)
	if !ok {
		return "", false
	}
	switch contracts.ToolType(call.Function.Name) {
	case contracts.BrowserClientsTool:
		return "list connected browsers", true
	case contracts.BrowserNavigateTool:
		return "navigate " + browserPreviewText(args.URL) + " in " + browserPreviewText(args.Client), true
	case contracts.BrowserReadTool:
		preview := ""
		switch args.How {
		case "text":
			preview = "read page text"
			if args.MaxChars > 0 {
				preview += fmt.Sprintf(" (max %d chars)", args.MaxChars)
			}
		case "screenshot":
			preview = "screenshot of the visible page"
		default:
			preview = "read page structure"
			if args.Page > 1 {
				preview += fmt.Sprintf(" (chunk %d)", args.Page)
			}
		}
		if args.WaitFor != "" {
			preview = fmt.Sprintf("wait for %q, then ", browserPreviewText(args.WaitFor)) + preview
		}
		return preview, true
	case contracts.BrowserActTool:
		switch args.By {
		case "point":
			switch args.Action {
			case "click":
				return "click point " + browserPoint(args), true
			case "type":
				preview := fmt.Sprintf("type %q at point %s", browserPreviewText(args.Text), browserPoint(args))
				if args.Submit {
					preview += " (then Enter)"
				}
				return preview, true
			}
			return "act at point " + browserPoint(args), true
		default:
			switch args.Action {
			case "click":
				return "click " + browserTarget(args), true
			case "type":
				preview := fmt.Sprintf("type %q into %s", browserPreviewText(args.Text), browserTarget(args))
				if args.Submit {
					preview += " (then Enter)"
				}
				return preview, true
			case "select":
				return fmt.Sprintf("select %q in %s", browserPreviewText(args.Value), browserTarget(args)), true
			}
			return "act on " + browserTarget(args), true
		}
	case contracts.BrowserScrollTool:
		preview := "scroll " + args.To
		if args.To == "" {
			preview = "scroll down"
		}
		if args.Times > 1 {
			preview += fmt.Sprintf(" %d screens", args.Times)
		}
		return preview, true
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
	const outlineHint = "The element is outlined on the page in the session's browser tab."
	const pointHint = "The agent cursor points at that spot in the session's browser tab."
	var lines []string
	switch contracts.ToolType(call.Function.Name) {
	case contracts.BrowserClientsTool:
		lines = []string{"BROWSER ACTION  LIST CONNECTED BROWSERS"}
	case contracts.BrowserNavigateTool:
		lines = []string{"BROWSER ACTION  NAVIGATE", "CLIENT  " + args.Client, "URL  " + args.URL}
	case contracts.BrowserReadTool:
		switch args.How {
		case "text":
			lines = []string{"BROWSER ACTION  READ PAGE TEXT"}
			if args.MaxChars > 0 {
				lines = append(lines, fmt.Sprintf("MAX CHARS  %d", args.MaxChars))
			}
		case "screenshot":
			lines = []string{"BROWSER ACTION  VIEWPORT SCREENSHOT", "Captures the visible part of the session's browser tab and hands it to the agent."}
		default:
			lines = []string{"BROWSER ACTION  READ PAGE STRUCTURE"}
			if args.Page > 1 {
				lines = append(lines, fmt.Sprintf("CHUNK  %d", args.Page))
			}
		}
		if args.WaitFor != "" {
			lines = append(lines, fmt.Sprintf("WAIT FOR  %q", args.WaitFor))
		}
	case contracts.BrowserActTool:
		if args.By == "point" {
			lines = []string{"BROWSER ACTION  " + strings.ToUpper(args.Action) + " AT POINT", "POINT  " + browserPoint(args)}
		} else {
			lines = []string{"BROWSER ACTION  " + strings.ToUpper(args.Action), "TARGET  " + browserTarget(args)}
		}
		switch args.Action {
		case "type":
			submit := "no"
			if args.Submit {
				submit = "yes (press Enter after typing)"
			}
			lines = append(lines, fmt.Sprintf("TEXT  %q", args.Text), "SUBMIT  "+submit)
		case "select":
			lines = append(lines, fmt.Sprintf("VALUE  %q", args.Value))
		}
		if args.By == "point" {
			lines = append(lines, pointHint)
		} else {
			lines = append(lines, outlineHint)
		}
	case contracts.BrowserScrollTool:
		to := args.To
		if to == "" {
			to = "down"
		}
		lines = []string{"BROWSER ACTION  SCROLL", "TO  " + to}
		if args.Times > 1 {
			lines = append(lines, fmt.Sprintf("TIMES  %d screens", args.Times))
		}
	default:
		return "", false
	}
	return strings.Join(lines, "\n"), true
}
