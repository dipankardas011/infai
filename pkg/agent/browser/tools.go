package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/google/uuid"
)

// Driver is what a session needs from the bridge. A nil Driver means the
// session offers no browser tools.
type Driver interface {
	Do(ctx context.Context, sessionID uuid.UUID, sessionName, action string, args any) (json.RawMessage, error)
	Notify(event string, data any)
}

const refHint = "Refs (e1, e2, ...) come from the latest browser_snapshot and expire when the page changes."

func Tools() []contracts.Tool {
	ref := map[string]any{"type": "string", "description": "Element ref from the latest browser_snapshot, e.g. e12"}
	element := map[string]any{"type": "string", "description": `Human-readable description of the element shown to the approver, e.g. button "Place order"`}
	return []contracts.Tool{
		{
			Name:        string(contracts.BrowserNavigateTool),
			Description: "Open a URL in this session's tab of the user's Chrome (created on first use). Follow with browser_snapshot to see the page.",
			Parameters: contracts.ToolParameterObjectSchema(map[string]any{
				"url": map[string]any{"type": "string", "description": "Absolute http:// or https:// URL"},
			}, []string{"url"}),
		},
		{
			Name:        string(contracts.BrowserSnapshotTool),
			Description: "Read the current page as an accessibility tree: one line per element, interactive ones prefixed with a ref. " + refHint,
			Parameters:  contracts.ToolParameterObjectSchema(nil, nil),
		},
		{
			Name:        string(contracts.BrowserReadTool),
			Description: "Read the visible text of the current page. Use for reading content; use browser_snapshot to find elements to act on.",
			Parameters: contracts.ToolParameterObjectSchema(map[string]any{
				"max_chars": map[string]any{"type": "integer", "description": "Maximum characters to return; at most 20000"},
			}, nil),
		},
		{
			Name:        string(contracts.BrowserWaitForTool),
			Description: "Wait until the given text appears on the page.",
			Parameters: contracts.ToolParameterObjectSchema(map[string]any{
				"text":       map[string]any{"type": "string", "description": "Text to wait for"},
				"timeout_ms": map[string]any{"type": "integer", "description": "Defaults to 10000; at most 30000"},
			}, []string{"text"}),
		},
		{
			Name:        string(contracts.BrowserClickTool),
			Description: "Click an element. " + refHint,
			Parameters:  contracts.ToolParameterObjectSchema(map[string]any{"ref": ref, "element": element}, []string{"ref", "element"}),
		},
		{
			Name:        string(contracts.BrowserTypeTool),
			Description: "Replace the content of an input with text, optionally pressing Enter afterwards. " + refHint,
			Parameters: contracts.ToolParameterObjectSchema(map[string]any{
				"ref":     ref,
				"element": element,
				"text":    map[string]any{"type": "string", "description": "Text to type"},
				"submit":  map[string]any{"type": "boolean", "description": "Press Enter after typing"},
			}, []string{"ref", "element", "text"}),
		},
		{
			Name:        string(contracts.BrowserSelectTool),
			Description: "Choose an option of a <select> by value or visible label. " + refHint,
			Parameters: contracts.ToolParameterObjectSchema(map[string]any{
				"ref":     ref,
				"element": element,
				"value":   map[string]any{"type": "string", "description": "Option value or label"},
			}, []string{"ref", "element", "value"}),
		},
	}
}

func IsTool(name contracts.ToolType) bool {
	switch name {
	case contracts.BrowserNavigateTool, contracts.BrowserSnapshotTool, contracts.BrowserReadTool,
		contracts.BrowserWaitForTool, contracts.BrowserClickTool, contracts.BrowserTypeTool, contracts.BrowserSelectTool:
		return true
	}
	return false
}

// isActing reports tools that act on one element, which the extension outlines
// on the page while the human decides.
func isActing(name contracts.ToolType) bool {
	return name == contracts.BrowserClickTool || name == contracts.BrowserTypeTool || name == contracts.BrowserSelectTool
}

type pageResult struct {
	TabID int    `json:"tab_id"`
	URL   string `json:"url"`
	Title string `json:"title"`
	Text  string `json:"text"`
	Found bool   `json:"found"`
}

// Execute runs one browser tool call for a session and returns the text the model reads.
func Execute(ctx context.Context, d Driver, sessionID uuid.UUID, sessionName string, call contracts.ToolCall) (string, error) {
	name := call.Function.Name
	action, args, err := actionFor(call)
	if err != nil {
		return "", err
	}
	if d == nil {
		return "", toolError(name, ErrNotConnected)
	}

	raw, err := d.Do(ctx, sessionID, sessionName, action, args)
	if err != nil {
		return "", toolError(name, err)
	}
	var page pageResult
	if err := json.Unmarshal(raw, &page); err != nil {
		return "", contracts.NewToolExecutionError(name, "bad_extension_reply", "the browser extension returned an unreadable result", contracts.ResponsibilityEnvironment, err)
	}

	switch name {
	case contracts.BrowserNavigateTool:
		return fmt.Sprintf("Opened %s (%q) in tab %d. Call browser_snapshot to see its elements.", page.URL, page.Title, page.TabID), nil
	case contracts.BrowserSnapshotTool:
		return fmt.Sprintf("Page: %s\nTitle: %s\n\n%s\n\n%s", page.URL, page.Title, page.Text, refHint), nil
	case contracts.BrowserReadTool:
		return fmt.Sprintf("Page: %s\nTitle: %s\n\n%s", page.URL, page.Title, page.Text), nil
	case contracts.BrowserWaitForTool:
		if page.Found {
			return fmt.Sprintf("The text appeared on %s.", page.URL), nil
		}
		return fmt.Sprintf("The text did not appear on %s before the timeout.", page.URL), nil
	default:
		return fmt.Sprintf("Done. The page is now %s (%q). Earlier refs may be stale; call browser_snapshot before the next action.", page.URL, page.Title), nil
	}
}

// Highlight outlines the element an acting tool call targets. It is a no-op
// for other tools.
func Highlight(ctx context.Context, d Driver, sessionID uuid.UUID, sessionName string, call contracts.ToolCall) error {
	if d == nil || !isActing(call.Function.Name) {
		return nil
	}
	var args struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil || args.Ref == "" {
		return nil
	}
	verb := map[contracts.ToolType]string{
		contracts.BrowserClickTool:  "click this",
		contracts.BrowserTypeTool:   "type here",
		contracts.BrowserSelectTool: "choose an option here",
	}[call.Function.Name]
	_, err := d.Do(ctx, sessionID, sessionName, "highlight", map[string]string{"ref": args.Ref, "label": "wants to " + verb})
	return err
}

func ClearHighlight(ctx context.Context, d Driver, sessionID uuid.UUID, sessionName string, call contracts.ToolCall) error {
	if d == nil || !isActing(call.Function.Name) {
		return nil
	}
	_, err := d.Do(ctx, sessionID, sessionName, "clear_highlight", nil)
	return err
}

func actionFor(call contracts.ToolCall) (string, any, error) {
	name := call.Function.Name
	invalid := func(reason string) error {
		return contracts.NewToolExecutionError(name, "invalid_arguments", reason, contracts.ResponsibilityAgent, nil)
	}

	switch name {
	case contracts.BrowserNavigateTool:
		args, err := contracts.DecodeToolArguments[struct {
			URL string `json:"url"`
		}](name, call)
		if err != nil {
			return "", nil, err
		}
		if u, err := url.Parse(args.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", nil, invalid("url must be an absolute http:// or https:// URL")
		}
		return "navigate", map[string]string{"url": args.URL}, nil

	case contracts.BrowserSnapshotTool:
		if _, err := contracts.DecodeToolArguments[struct{}](name, call); err != nil {
			return "", nil, err
		}
		return "snapshot", nil, nil

	case contracts.BrowserReadTool:
		args, err := contracts.DecodeToolArguments[struct {
			MaxChars *int `json:"max_chars,omitempty"`
		}](name, call)
		if err != nil {
			return "", nil, err
		}
		forward := map[string]int{}
		if args.MaxChars != nil {
			forward["max_chars"] = *args.MaxChars
		}
		return "read", forward, nil

	case contracts.BrowserWaitForTool:
		args, err := contracts.DecodeToolArguments[struct {
			Text      string `json:"text"`
			TimeoutMS *int   `json:"timeout_ms,omitempty"`
		}](name, call)
		if err != nil {
			return "", nil, err
		}
		if strings.TrimSpace(args.Text) == "" {
			return "", nil, invalid("text is required")
		}
		forward := map[string]any{"text": args.Text}
		if args.TimeoutMS != nil {
			forward["timeout_ms"] = *args.TimeoutMS
		}
		return "wait_for", forward, nil

	case contracts.BrowserClickTool:
		args, err := contracts.DecodeToolArguments[struct {
			Ref     string `json:"ref"`
			Element string `json:"element"`
		}](name, call)
		if err != nil {
			return "", nil, err
		}
		if args.Ref == "" {
			return "", nil, invalid("ref is required")
		}
		return "click", map[string]string{"ref": args.Ref}, nil

	case contracts.BrowserTypeTool:
		args, err := contracts.DecodeToolArguments[struct {
			Ref     string `json:"ref"`
			Element string `json:"element"`
			Text    string `json:"text"`
			Submit  bool   `json:"submit"`
		}](name, call)
		if err != nil {
			return "", nil, err
		}
		if args.Ref == "" {
			return "", nil, invalid("ref is required")
		}
		return "type", map[string]any{"ref": args.Ref, "text": args.Text, "submit": args.Submit}, nil

	case contracts.BrowserSelectTool:
		args, err := contracts.DecodeToolArguments[struct {
			Ref     string `json:"ref"`
			Element string `json:"element"`
			Value   string `json:"value"`
		}](name, call)
		if err != nil {
			return "", nil, err
		}
		if args.Ref == "" {
			return "", nil, invalid("ref is required")
		}
		return "select", map[string]string{"ref": args.Ref, "value": args.Value}, nil
	}
	return "", nil, contracts.NewToolExecutionError(name, "unknown_tool", "not a browser tool", contracts.ResponsibilityAgent, nil)
}

// toolError translates bridge failures into errors the model can act on.
func toolError(name contracts.ToolType, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrNotConnected) {
		return contracts.NewToolExecutionError(name, "browser_not_connected",
			"the infai Chrome extension is not connected to the daemon; ask the user to open Chrome with the extension loaded, and check `infaiw browser status` and the extension's side panel",
			contracts.ResponsibilityEnvironment, err)
	}
	var ae *ActionError
	if !errors.As(err, &ae) {
		return contracts.NewToolExecutionError(name, "browser_error", err.Error(), contracts.ResponsibilityEnvironment, err)
	}
	switch ae.Code {
	case "no_tab":
		return contracts.NewToolExecutionError(name, ae.Code, ae.Message+"; call browser_navigate first", contracts.ResponsibilityAgent, err)
	case "stale_ref", "invalid_args":
		return contracts.NewToolExecutionError(name, ae.Code, ae.Message, contracts.ResponsibilityAgent, err)
	case "tab_closed":
		return contracts.NewToolExecutionError(name, ae.Code, ae.Message+"; the user closed the tab, browser_navigate opens a new one", contracts.ResponsibilityUser, err)
	default:
		return contracts.NewToolExecutionError(name, ae.Code, ae.Message, contracts.ResponsibilityEnvironment, err)
	}
}
