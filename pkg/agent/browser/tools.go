package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/vision"
	"github.com/google/uuid"
)

const (
	snapshotMaxPage = 10
	scrollMaxTimes  = 20
	readMaxChars    = 20000
	maxPointCoord   = 20000
)

const refHint = "Refs (e1, e2, ...) come from the latest browser_read with how=elements and expire when the page changes."

func Tools() []contracts.Tool {
	ref := map[string]any{"type": "string", "description": "Element ref from the latest browser_read how=elements, e.g. e12"}
	element := map[string]any{"type": "string", "description": `Human-readable description of the target, shown to the approver, e.g. button "Place order"`}
	return []contracts.Tool{
		{
			Name:        string(contracts.BrowserClientsTool),
			Description: "List the user's connected browsers. Each Chromium browser profile that loaded the infai extension is one client. Call before the first browser_navigate to learn the client ids.",
			Parameters:  contracts.ToolParameterObjectSchema(nil, nil),
		},
		{
			Name:        string(contracts.BrowserNavigateTool),
			Description: "Open a URL in this session's tab of one connected browser (the tab is created on first use). Later browser_* calls act in that tab until another navigate names a different client. Follow with browser_read.",
			Parameters: contracts.ToolParameterObjectSchema(map[string]any{
				"url":    map[string]any{"type": "string", "description": "Absolute http:// or https:// URL"},
				"client": map[string]any{"type": "string", "description": `Browser id from browser_clients, e.g. "brave__personal"; words that match only one id ("brave") also work. When more than one is connected and the user has not said which, ask them.`},
			}, []string{"url", "client"}),
		},
		{
			Name:        string(contracts.BrowserReadTool),
			Description: "Read this session's page without changing it. how=elements: accessibility tree, one line per element, interactive ones prefixed with a ref (default). how=text: the page's visible text. how=screenshot: the visible viewport as a PNG, the fallback for pages whose tree is empty or misleading (canvas, custom widgets); aim browser_act at its pixels with by=point.",
			Parameters: contracts.ToolParameterObjectSchema(map[string]any{
				"how":        map[string]any{"type": "string", "enum": []string{"elements", "text", "screenshot"}, "description": "What to return. Defaults to elements"},
				"page":       map[string]any{"type": "integer", "description": "how=elements: chunk of a long page. Defaults to 1; at most 10"},
				"max_chars":  map[string]any{"type": "integer", "description": "how=text: maximum characters. At most 20000"},
				"wait_for":   map[string]any{"type": "string", "description": "Wait until this text appears before reading"},
				"timeout_ms": map[string]any{"type": "integer", "description": "With wait_for: how long to wait. Defaults to 10000; at most 30000"},
			}, nil),
		},
		{
			Name:        string(contracts.BrowserActTool),
			Description: "Change this session's page; every call waits for the user's approval. by=ref targets an element from the latest browser_read how=elements. by=point targets a pixel of the latest browser_read how=screenshot (fallback only when the page has no usable refs). action=click presses it, action=type replaces its text, action=select picks an option of a <select> (by=ref only).",
			Parameters: contracts.ToolParameterObjectSchema(map[string]any{
				"by":      map[string]any{"type": "string", "enum": []string{"ref", "point"}, "description": "How the target is identified"},
				"action":  map[string]any{"type": "string", "enum": []string{"click", "type", "select"}, "description": "What to do at the target"},
				"element": element,
				"ref":     ref,
				"x":       map[string]any{"type": "integer", "description": "by=point: horizontal pixel offset in the latest screenshot"},
				"y":       map[string]any{"type": "integer", "description": "by=point: vertical pixel offset in the latest screenshot"},
				"text":    map[string]any{"type": "string", "description": "action=type: the text to enter"},
				"value":   map[string]any{"type": "string", "description": "action=select: option value or visible label"},
				"submit":  map[string]any{"type": "boolean", "description": "action=type: press Enter afterwards"},
			}, []string{"by", "action", "element"}),
		},
		{
			Name:        string(contracts.BrowserScrollTool),
			Description: "Scroll this session's page, e.g. to load more of a list that loads on scroll. Acting on an element ref scrolls to it already, so use this only when you want to move the page itself.",
			Parameters: contracts.ToolParameterObjectSchema(map[string]any{
				"to":    map[string]any{"type": "string", "enum": []string{"down", "up", "top", "bottom"}, "description": "Where to scroll. Defaults to down"},
				"times": map[string]any{"type": "integer", "description": "Screens to scroll for down/up. Defaults to 1; at most 20"},
			}, nil),
		},
	}
}

func IsTool(name contracts.ToolType) bool {
	switch name {
	case contracts.BrowserClientsTool, contracts.BrowserNavigateTool, contracts.BrowserReadTool,
		contracts.BrowserActTool, contracts.BrowserScrollTool:
		return true
	}
	return false
}

// actArgs is one browser_act call. by selects the target space; action selects
// the verb. Both are required so the model states its intent instead of having
// it inferred from whichever fields happen to be set.
type actArgs struct {
	By      string `json:"by"`
	Action  string `json:"action"`
	Element string `json:"element"`
	Ref     string `json:"ref"`
	X       *int   `json:"x"`
	Y       *int   `json:"y"`
	Text    string `json:"text"`
	Value   string `json:"value"`
	Submit  bool   `json:"submit"`
}

// readArgs is one browser_read call.
type readArgs struct {
	How       string `json:"how"`
	Page      *int   `json:"page"`
	MaxChars  *int   `json:"max_chars"`
	WaitFor   string `json:"wait_for"`
	TimeoutMS *int   `json:"timeout_ms"`
}

type pageResult struct {
	TabID int    `json:"tab_id"`
	URL   string `json:"url"`
	Title string `json:"title"`
	Text  string `json:"text"`
	Found bool   `json:"found"`
	Page  int    `json:"page"`
	Pages int    `json:"pages"`

	Before int `json:"before"`
	After  int `json:"after"`
	Max    int `json:"max"`

	Width   int     `json:"width"`
	Height  int     `json:"height"`
	Scale   float64 `json:"scale"`
	ScrollY int     `json:"scroll_y"`
	ImageID string  `json:"image_id"`
	X       int     `json:"x"`
	Y       int     `json:"y"`
}

// Output is what a browser tool returns: the text the model reads and any
// images (a viewport screenshot) that ride with it.
type Output struct {
	Text   string
	Images []contracts.ImageInput
}

// step is one extension action a tool call turns into.
type step struct {
	action Action
	args   any
}

// waitArgs is the extension's wait_for payload.
type waitArgs struct {
	Text      string `json:"text"`
	TimeoutMS *int   `json:"timeout_ms,omitempty"`
}

// execute runs one browser tool call for a session.
func execute(ctx context.Context, d *Bridge, sessionID uuid.UUID, sessionName string, call contracts.ToolCall) (Output, error) {
	name := call.Function.Name
	if name == contracts.BrowserClientsTool {
		if _, err := contracts.DecodeToolArguments[struct{}](name, call); err != nil {
			return Output{}, err
		}
		text, err := listClients(d)
		return Output{Text: text}, err
	}
	steps, bindClient, err := plan(call)
	if err != nil {
		return Output{}, err
	}
	if d == nil {
		return Output{}, toolError(name, ErrNotConnected)
	}

	var client Client
	if bindClient != "" {
		if client, err = d.Bind(ctx, sessionID, sessionName, bindClient); err != nil {
			return Output{}, toolError(name, err)
		}
	}

	// wait_for is a read precondition, so it runs before the read itself.
	if wait, ok := steps[0].args.(waitArgs); ok {
		raw, werr := d.Do(ctx, sessionID, sessionName, "wait_for", wait)
		if werr != nil {
			return Output{}, toolError(name, werr)
		}
		var w pageResult
		if json.Unmarshal(raw, &w) == nil && !w.Found {
			return Output{Text: fmt.Sprintf("The text %q did not appear on %s before the timeout; nothing was read.", wait.Text, w.URL)}, nil
		}
		steps = steps[1:]
	}

	raw, err := d.Do(ctx, sessionID, sessionName, steps[0].action, steps[0].args)
	if err != nil {
		return Output{}, toolError(name, err)
	}
	var page pageResult
	if err := json.Unmarshal(raw, &page); err != nil {
		return Output{}, contracts.NewToolExecutionError(name, "bad_extension_reply", "the browser extension returned an unreadable result", contracts.ResponsibilityEnvironment, err)
	}
	return describe(d, call, page, client)
}

// plan validates a call and returns the extension steps it needs, plus the
// client to bind (only browser_navigate sets it).
func plan(call contracts.ToolCall) ([]step, string, error) {
	switch call.Function.Name {
	case contracts.BrowserNavigateTool:
		args, err := contracts.DecodeToolArguments[struct {
			URL    string `json:"url"`
			Client string `json:"client"`
		}](call.Function.Name, call)
		if err != nil {
			return nil, "", err
		}
		if u, err := url.Parse(args.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, "", invalidArg(call, "url must be an absolute http:// or https:// URL")
		}
		if strings.TrimSpace(args.Client) == "" {
			return nil, "", invalidArg(call, "client is required; call browser_clients to see the connected browsers")
		}
		return []step{{action: ActionNavigate, args: map[string]string{"url": args.URL}}}, args.Client, nil

	case contracts.BrowserReadTool:
		args, err := contracts.DecodeToolArguments[readArgs](call.Function.Name, call)
		if err != nil {
			return nil, "", err
		}
		steps, err := readSteps(call, args)
		return steps, "", err

	case contracts.BrowserActTool:
		args, err := contracts.DecodeToolArguments[actArgs](call.Function.Name, call)
		if err != nil {
			return nil, "", err
		}
		s, err := actStep(call, args)
		return []step{s}, "", err

	case contracts.BrowserScrollTool:
		args, err := contracts.DecodeToolArguments[struct {
			To    string `json:"to"`
			Times *int   `json:"times,omitempty"`
		}](call.Function.Name, call)
		if err != nil {
			return nil, "", err
		}
		to := args.To
		if to == "" {
			to = "down"
		}
		if !slices.Contains([]string{"down", "up", "top", "bottom"}, to) {
			return nil, "", invalidArg(call, "to must be one of: down, up, top, bottom")
		}
		forward := map[string]any{"to": to}
		if args.Times != nil {
			if *args.Times < 1 || *args.Times > scrollMaxTimes {
				return nil, "", invalidArg(call, fmt.Sprintf("times must be between 1 and %d", scrollMaxTimes))
			}
			forward["times"] = *args.Times
		}
		return []step{{action: ActionScroll, args: forward}}, "", nil
	}
	return nil, "", contracts.NewToolExecutionError(call.Function.Name, "unknown_tool", "not a browser tool", contracts.ResponsibilityAgent, nil)
}

func readSteps(call contracts.ToolCall, args readArgs) ([]step, error) {
	how := args.How
	if how == "" {
		how = "elements"
	}
	var steps []step
	if strings.TrimSpace(args.WaitFor) != "" {
		if args.TimeoutMS != nil && (*args.TimeoutMS < 1 || *args.TimeoutMS > 30000) {
			return nil, invalidArg(call, "timeout_ms must be between 1 and 30000")
		}
		wait := waitArgs{Text: args.WaitFor}
		if args.TimeoutMS != nil {
			wait.TimeoutMS = args.TimeoutMS
		}
		steps = append(steps, step{action: ActionWaitFor, args: wait})
	}
	switch how {
	case "elements":
		forward := map[string]int{}
		if args.Page != nil {
			if *args.Page < 1 || *args.Page > snapshotMaxPage {
				return nil, invalidArg(call, fmt.Sprintf("page must be between 1 and %d", snapshotMaxPage))
			}
			forward["page"] = *args.Page
		}
		return append(steps, step{action: ActionSnapshot, args: forward}), nil
	case "text":
		forward := map[string]int{}
		if args.MaxChars != nil {
			if *args.MaxChars < 1 || *args.MaxChars > readMaxChars {
				return nil, invalidArg(call, fmt.Sprintf("max_chars must be between 1 and %d", readMaxChars))
			}
			forward["max_chars"] = *args.MaxChars
		}
		return append(steps, step{action: ActionRead, args: forward}), nil
	case "screenshot":
		return append(steps, step{action: ActionScreenshot, args: nil}), nil
	}
	return nil, invalidArg(call, "how must be one of: elements, text, screenshot")
}

func actStep(call contracts.ToolCall, args actArgs) (step, error) {
	switch args.By {
	case "ref":
		if args.Ref == "" {
			return step{}, invalidArg(call, "ref is required when by=ref")
		}
		switch args.Action {
		case "click":
			return step{action: ActionClick, args: map[string]string{"ref": args.Ref}}, nil
		case "type":
			return step{action: ActionType, args: map[string]any{"ref": args.Ref, "text": args.Text, "submit": args.Submit}}, nil
		case "select":
			return step{action: ActionSelect, args: map[string]string{"ref": args.Ref, "value": args.Value}}, nil
		}
		return step{}, invalidArg(call, "action must be one of: click, type, select")
	case "point":
		if args.X == nil || args.Y == nil {
			return step{}, invalidArg(call, "x and y are required when by=point")
		}
		if err := validatePoint(call.Function.Name, *args.X, *args.Y); err != nil {
			return step{}, err
		}
		switch args.Action {
		case "click":
			return step{action: ActionClickAt, args: map[string]int{"x": *args.X, "y": *args.Y}}, nil
		case "type":
			return step{action: ActionTypeAt, args: map[string]any{"x": *args.X, "y": *args.Y, "text": args.Text, "submit": args.Submit}}, nil
		case "select":
			return step{}, invalidArg(call, "a <select> cannot be driven by a point; use by=ref with action=select")
		}
		return step{}, invalidArg(call, "action must be one of: click, type, select")
	}
	return step{}, invalidArg(call, "by must be ref or point")
}

func describe(d *Bridge, call contracts.ToolCall, page pageResult, client Client) (Output, error) {
	switch call.Function.Name {
	case contracts.BrowserNavigateTool:
		return Output{Text: fmt.Sprintf("Opened %s (%q) in %s, tab %d. Call browser_read to see its elements.", page.URL, page.Title, client.ID, page.TabID)}, nil
	case contracts.BrowserReadTool:
		return readOutput(d, call, page)
	case contracts.BrowserScrollTool:
		if page.After == page.Before {
			return Output{Text: fmt.Sprintf("The page did not move (scroll offset %d of %d): it is already at that edge, or its content sits in an inner scroll box. "+
				"Act on an element ref instead; that scrolls to the element itself.", page.After, page.Max)}, nil
		}
		return Output{Text: fmt.Sprintf("Scrolled from %d to %d of %d on %s (%q). Call browser_read to see what loaded.", page.Before, page.After, page.Max, page.URL, page.Title)}, nil
	}
	// browser_act
	if page.X != 0 || page.Y != 0 {
		return Output{Text: fmt.Sprintf("Done at (%d, %d) on %s (%q). Take a new browser_read how=screenshot before the next point action.", page.X, page.Y, page.URL, page.Title)}, nil
	}
	return Output{Text: fmt.Sprintf("Done. The page is now %s (%q). Earlier refs may be stale; call browser_read before the next action.", page.URL, page.Title)}, nil
}

func readOutput(d *Bridge, call contracts.ToolCall, page pageResult) (Output, error) {
	var args readArgs
	if raw := strings.TrimSpace(call.Function.Arguments); raw != "" {
		_ = json.Unmarshal([]byte(raw), &args)
	}
	switch args.How {
	case "text":
		return Output{Text: fmt.Sprintf("Page: %s\nTitle: %s\n\n%s", page.URL, page.Title, page.Text)}, nil
	case "screenshot":
		return screenshotOutput(d, page)
	}
	where := ""
	if page.Pages > 1 {
		where = fmt.Sprintf(" (chunk %d of %d)", page.Page, page.Pages)
	}
	return Output{Text: fmt.Sprintf("Page: %s\nTitle: %s%s\n\n%s\n\n%s", page.URL, page.Title, where, page.Text, refHint)}, nil
}

// screenshotOutput turns the extension's base64 image into an attachment the
// model receives with the tool result. It goes through the same validator as a
// user-attached image, so the limits and the accepted formats are one set.
func screenshotOutput(d *Bridge, page pageResult) (Output, error) {
	if page.ImageID == "" {
		return Output{}, contracts.NewToolExecutionError(contracts.BrowserReadTool, "bad_extension_reply",
			"the browser extension returned no screenshot id", contracts.ResponsibilityEnvironment, nil)
	}
	raw, ok := d.TakeImage(page.ImageID)
	if !ok {
		return Output{}, contracts.NewToolExecutionError(contracts.BrowserReadTool, "screenshot_expired",
			"the screenshot the extension uploaded is no longer available; take it again",
			contracts.ResponsibilityEnvironment, nil)
	}
	image, err := vision.ValidateImage(raw)
	if err != nil {
		return Output{}, contracts.NewToolExecutionError(contracts.BrowserReadTool, "bad_extension_reply",
			"the browser extension returned an unusable screenshot: "+err.Error(), contracts.ResponsibilityEnvironment, err)
	}
	text := fmt.Sprintf("Visible part of %s (%q) as a %dx%d %s", page.URL, page.Title, image.Width, image.Height, image.MediaType)
	if page.Scale > 0 && page.Scale < 1 {
		text += fmt.Sprintf(" (scaled to %d%% of the page)", int(page.Scale*100+0.5))
	}
	if page.ScrollY > 0 {
		text += fmt.Sprintf(", scrolled %dpx down", page.ScrollY)
	}
	text += ". Aim browser_act at this image's pixels with by=point (origin 0,0); they are mapped to the page for you."
	image.Name = "viewport." + strings.TrimPrefix(image.MediaType, "image/")
	return Output{Text: text, Images: []contracts.ImageInput{image}}, nil
}

// Highlight shows where an approved act will land: the element outlined by ref,
// or the agent cursor parked on the point.
func highlight(ctx context.Context, d *Bridge, sessionID uuid.UUID, sessionName string, call contracts.ToolCall) error {
	if d == nil || call.Function.Name != contracts.BrowserActTool {
		return nil
	}
	var args actArgs
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return nil
	}
	verb := map[string]string{"click": "click this", "type": "type here", "select": "choose an option here"}[args.Action]
	if verb == "" {
		return nil
	}
	if args.By == "point" {
		if args.X == nil || args.Y == nil {
			return nil
		}
		_, err := d.Do(ctx, sessionID, sessionName, ActionHighlight, map[string]any{"x": *args.X, "y": *args.Y, "label": "wants to " + verb})
		return err
	}
	if args.Ref == "" {
		return nil
	}
	_, err := d.Do(ctx, sessionID, sessionName, ActionHighlight, map[string]string{"ref": args.Ref, "label": "wants to " + verb})
	return err
}

func clearHighlight(ctx context.Context, d *Bridge, sessionID uuid.UUID, sessionName string, call contracts.ToolCall) error {
	if d == nil || call.Function.Name != contracts.BrowserActTool {
		return nil
	}
	_, err := d.Do(ctx, sessionID, sessionName, ActionClearHighlight, nil)
	return err
}

func listClients(d *Bridge) (string, error) {
	var clients []Client
	if d != nil {
		clients = d.Clients()
	}
	if len(clients) == 0 {
		return "", toolError(contracts.BrowserClientsTool, ErrNotConnected)
	}
	var sb strings.Builder
	sb.WriteString("Connected browsers (pass one as client to browser_navigate):\n")
	for _, c := range clients {
		fmt.Fprintf(&sb, "- %s  (%d agent tabs)\n", c.ID, c.Tabs)
	}
	if len(clients) > 1 {
		sb.WriteString("If the user has not said which one to use, ask them.")
	}
	return strings.TrimSpace(sb.String()), nil
}

func invalidArg(call contracts.ToolCall, reason string) error {
	return contracts.NewToolExecutionError(call.Function.Name, "invalid_arguments", reason, contracts.ResponsibilityAgent, nil)
}

// validatePoint keeps a point inside a sane viewport; the exact bounds are the
// page's, but a negative or absurd coordinate is always a model mistake.
func validatePoint(tool contracts.ToolType, x, y int) error {
	if x < 0 || y < 0 || x > maxPointCoord || y > maxPointCoord {
		return contracts.NewToolExecutionError(tool, "invalid_arguments",
			fmt.Sprintf("x and y must be between 0 and %d", maxPointCoord), contracts.ResponsibilityAgent, nil)
	}
	return nil
}

// toolError translates bridge failures into errors the model can act on.
func toolError(name contracts.ToolType, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrNotConnected) {
		return contracts.NewToolExecutionError(name, "browser_not_connected",
			"no browser with the infai extension is connected to the daemon; ask the user to open one (Chrome, Brave, Edge, ...) with the extension loaded, and check `infaiw browser status`",
			contracts.ResponsibilityEnvironment, err)
	}
	if strings.Contains(err.Error(), "no response from extension") {
		return contracts.NewToolExecutionError(name, "browser_unresponsive",
			"the browser extension did not answer in time ("+err.Error()+"); bring its tab to the front (side panel: jump to tab) and retry, and reload the extension if this repeats",
			contracts.ResponsibilityEnvironment, err)
	}
	var ae *ActionError
	if !errors.As(err, &ae) {
		return contracts.NewToolExecutionError(name, "browser_error", err.Error(), contracts.ResponsibilityEnvironment, err)
	}
	switch ae.Code {
	case "no_tab":
		return contracts.NewToolExecutionError(name, ae.Code, ae.Message+"; call browser_navigate first", contracts.ResponsibilityAgent, err)
	case "invalid_args":
		// An action the extension does not know is version skew, not a bad
		// argument: the daemon shipped new extension code and Chrome is still
		// running the old service worker.
		if strings.HasPrefix(ae.Message, "unknown action:") {
			return contracts.NewToolExecutionError(name, "extension_outdated",
				"the browser extension does not support this action yet; reload it in chrome://extensions (the daemon rewrites its files at startup, but Chrome keeps the loaded code until Reload)",
				contracts.ResponsibilityUser, err)
		}
		return contracts.NewToolExecutionError(name, ae.Code, ae.Message, contracts.ResponsibilityAgent, err)
	case "stale_ref":
		return contracts.NewToolExecutionError(name, ae.Code, ae.Message, contracts.ResponsibilityAgent, err)
	case "client_required", "client_not_found", "client_ambiguous":
		return contracts.NewToolExecutionError(name, ae.Code, ae.Message+"; if the user has not said which browser to use, ask them", contracts.ResponsibilityAgent, err)
	case "upload_failed", "reply_too_large":
		return contracts.NewToolExecutionError(name, ae.Code, ae.Message, contracts.ResponsibilityEnvironment, err)
	case "client_disconnected":
		return contracts.NewToolExecutionError(name, ae.Code, ae.Message+"; ask the user to open it, or browser_navigate with another client", contracts.ResponsibilityUser, err)
	case "tab_closed":
		return contracts.NewToolExecutionError(name, ae.Code, ae.Message+"; the user closed the tab, browser_navigate opens a new one", contracts.ResponsibilityUser, err)
	default:
		return contracts.NewToolExecutionError(name, ae.Code, ae.Message, contracts.ResponsibilityEnvironment, err)
	}
}
