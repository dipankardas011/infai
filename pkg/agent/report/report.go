// Package report renders complete agent session timelines as self-contained HTML.
package report

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"sort"
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// SessionTimeline is one session's complete exportable history.
type SessionTimeline struct {
	Meta   store.SessionMeta
	Events []store.Event
	Head   uuid.UUID
}

type kv struct{ Key, Value string }

type toolCallView struct {
	ID, Name, Args, Anchor string
	Command                string
	ArgRows                []kv
}

type toolResultView struct {
	ExitCode string
	Failed   bool
	Output   string
	Rows     []kv
}

type imageView struct {
	Name, MediaType, Prompt string
	Size                    int
	Width, Height           int
	Src                     template.URL
}

type displayView struct {
	Role, Text string
	Badges     []string
	Current    bool
}

type eventView struct {
	Index                 int
	Depth                 int
	Guides                []int
	Enter                 bool
	ID, DOMID, ShortID    string
	ParentID, ParentDOMID string
	ParentShort, ForkFrom string
	Head, Active, Fork    bool
	Kind, Class, Label    string
	Icon, Time            string
	Displays              []displayView
	ForkPoint             bool
	ChildCount            int
	Text, Reasoning       template.HTML
	ToolCalls             []toolCallView
	ToolCallID, CallLink  string
	Status                string
	Result                *toolResultView
	Images                []imageView
	Summary               template.HTML
	Checklist, BlobHash   string
	Unresolved            bool
	Raw                   string
}

type treeNode struct {
	Event    *eventView
	Children []*treeNode
}

type treeRow struct {
	event  store.Event
	depth  int
	guides []int
	fork   bool
	enter  bool
}

type usageProgressView struct {
	Present                                   bool
	Prompt, Completion, Total, Window         uint64
	Cached, CacheWrite, Reasoning             uint64
	PromptPercent, CompletionPercent          float64
	PromptLabel, CompletionLabel, WindowLabel string
	Overflow                                  bool
}

type sessionView struct {
	Index, DOMID  string
	Title, Kind   string
	Icon          string
	Root          bool
	Meta, Counts  []kv
	UsageProgress usageProgressView
	Events        []*eventView
	Tree          []*treeNode
	Jumps         []*treeNode
}

type page struct {
	Title     string
	Generated string
	Sessions  []sessionView
}

var (
	markdown       = goldmark.New(goldmark.WithExtensions(extension.GFM))
	sanitizer      = bluemonday.UGCPolicy()
	safeImageTypes = map[string]bool{
		"image/png": true, "image/jpeg": true, "image/gif": true,
		"image/webp": true, "image/avif": true, "image/bmp": true,
	}
)

// Render builds a report for one session. Use RenderSessions when offspring
// timelines should be available in the session switcher.
func Render(meta store.SessionMeta, events []store.Event, head uuid.UUID) ([]byte, error) {
	return RenderSessions([]SessionTimeline{{Meta: meta, Events: events, Head: head}})
}

// RenderSessions builds one self-contained report containing the root session
// followed by each offspring timeline.
func RenderSessions(sessions []SessionTimeline) ([]byte, error) {
	if len(sessions) == 0 {
		return nil, fmt.Errorf("report: no sessions")
	}

	data := page{
		Title:     sessionTitle(sessions[0].Meta),
		Generated: time.Now().UTC().Format(time.RFC3339),
		Sessions:  make([]sessionView, 0, len(sessions)),
	}
	for i, session := range sessions {
		data.Sessions = append(data.Sessions, renderSession(i, session))
	}

	var buf bytes.Buffer
	if err := pageTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("report: render: %w", err)
	}
	return buf.Bytes(), nil
}

func renderSession(index int, session SessionTimeline) sessionView {
	events := session.Events
	active := activeSet(events, session.Head)
	children := childCounts(events)
	toolNames, toolAnchors := toolCalls(events, index)
	rows := timelineTreeRows(events)
	ordered := make([]store.Event, 0, len(rows))
	views := make([]*eventView, 0, len(rows))
	byID := make(map[uuid.UUID]*eventView, len(rows))
	counts := map[string]int{}
	branchPoints, branchArms := 0, 0

	for i, row := range rows {
		event := row.event
		ordered = append(ordered, event)
		view := buildEvent(index, i+1, event, toolNames, toolAnchors)
		view.ChildCount = children[event.ID]
		view.ForkPoint = view.ChildCount > 1
		view.Head = event.ID == session.Head
		view.Active = active[event.ID]
		if event.BranchFrom != nil {
			branchArms++
			view.Fork = true
			view.ForkFrom = short(*event.BranchFrom)
		}
		if view.ForkPoint {
			branchPoints++
		}
		view.Depth, view.Guides, view.Enter = row.depth, row.guides, row.enter
		view.Displays = eventDisplays(event)
		if len(view.Displays) > 0 {
			if view.Fork {
				view.Displays[0].Badges = append(view.Displays[0].Badges, "branch")
			} else if view.Enter {
				view.Displays[0].Badges = append(view.Displays[0].Badges, "main")
			}
			if view.ForkPoint {
				view.Displays[0].Badges = append(view.Displays[0].Badges, "fork")
			}
			if view.Head {
				view.Displays[0].Badges = append(view.Displays[0].Badges, "head")
				view.Displays[0].Current = true
			}
		}
		counts[view.Kind+" / "+view.Class]++
		views = append(views, view)
		byID[event.ID] = view
	}

	countRows := []kv{
		{"events", fmt.Sprint(len(events))},
		{"on HEAD ancestry", fmt.Sprint(len(active))},
		{"branch points", fmt.Sprint(branchPoints)},
		{"branch arms", fmt.Sprint(branchArms)},
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		countRows = append(countRows, kv{key, fmt.Sprint(counts[key])})
	}

	kind := string(session.Meta.AgentKind)
	if kind == "" {
		kind = "session"
	}
	return sessionView{
		Index:         fmt.Sprint(index),
		DOMID:         fmt.Sprintf("session-%d", index),
		Title:         sessionTitle(session.Meta),
		Kind:          kind,
		Icon:          map[bool]string{true: "◉", false: "◇"}[index == 0],
		Root:          index == 0,
		Meta:          metaRows(session.Meta, session.Head),
		Counts:        countRows,
		UsageProgress: usageOf(session.Meta.LastTurnMeta),
		Events:        views,
		Tree:          branchOutline(ordered, byID, session.Head),
		Jumps:         turnIndex(ordered, byID),
	}
}

// timelineTreeRows is the TUI's branch-timeline layout: a depth-first walk that
// draws a connector for every arm and expands a linear run with │.
func timelineTreeRows(events []store.Event) []treeRow {
	children := make(map[uuid.UUID][]store.Event, len(events))
	for _, event := range events {
		children[event.ParentID] = append(children[event.ParentID], event)
	}
	for parent := range children {
		sort.SliceStable(children[parent], func(i, j int) bool {
			a, b := children[parent][i], children[parent][j]
			if (a.BranchFrom != nil) != (b.BranchFrom != nil) {
				return a.BranchFrom != nil
			}
			return a.ID.String() < b.ID.String()
		})
	}
	rows := make([]treeRow, 0, len(events))
	seen := make(map[uuid.UUID]struct{}, len(events))
	var visit func(store.Event, int, []int, bool, bool)
	visit = func(event store.Event, depth int, guides []int, fork, enter bool) {
		if _, exists := seen[event.ID]; exists {
			return
		}
		seen[event.ID] = struct{}{}
		rows = append(rows, treeRow{event: event, depth: depth, guides: guides, fork: fork, enter: enter})
		next := children[event.ID]
		if len(next) == 1 {
			// A linear run continues on the same rail: no new elbow.
			visit(next[0], depth, guides, false, false)
			return
		}
		for i, child := range next {
			childGuides := append([]int(nil), guides...)
			if i < len(next)-1 {
				childGuides = append(childGuides, depth)
			}
			visit(child, depth+1, childGuides, child.BranchFrom != nil, true)
		}
	}
	roots := children[uuid.Nil]
	for i, event := range roots {
		var guides []int
		if i < len(roots)-1 {
			guides = []int{0}
		}
		visit(event, 0, guides, false, false)
	}
	for _, event := range events {
		if _, exists := seen[event.ID]; !exists {
			visit(event, 0, nil, false, false)
		}
	}
	return rows
}

// eventDisplays mirrors the TUI: one event expands into thinking, assistant,
// tool_call and tool_result lines, so a busy turn stays readable.
func eventDisplays(event store.Event) []displayView {
	if event.Record == nil {
		if preview := event.Preview; preview != nil {
			role := preview.Role
			if role == "" {
				role = "assistant"
			}
			return []displayView{{Role: role, Text: previewText(preview.Text)}}
		}
		return []displayView{{Role: "assistant", Text: "content unavailable"}}
	}
	record := event.Record
	if record.Message != nil {
		message := record.Message
		switch message.Role {
		case "user":
			return []displayView{{Role: "user", Text: previewText(message.Text())}}
		case "tool":
			text := previewText(message.Text())
			if result := parseToolResult(message.Text()); result != nil {
				text = "exit " + result.ExitCode
				if output := previewText(result.Output); output != "" {
					text += " · " + output
				}
			}
			return []displayView{{Role: "tool_result", Text: text}}
		}
		displays := make([]displayView, 0, 2+len(message.ToolCalls))
		if message.ReasoningContent != "" {
			displays = append(displays, displayView{Role: "thinking", Text: previewText(message.ReasoningContent)})
		}
		if message.Text() != "" {
			displays = append(displays, displayView{Role: "assistant", Text: previewText(message.Text())})
		}
		for _, call := range message.ToolCalls {
			displays = append(displays, displayView{Role: "tool_call", Text: previewText(toolCallSummary(string(call.Function.Name), call.Function.Arguments))})
		}
		if len(displays) == 0 {
			displays = append(displays, displayView{Role: "assistant", Text: "empty response"})
		}
		return displays
	}
	if record.Compaction != nil {
		return []displayView{{Role: "assistant", Text: "context compacted: " + previewText(record.Compaction.Summary)}}
	}
	text := previewText(record.Text)
	if text == "" {
		text = strings.ReplaceAll(string(event.Kind), "_", " ")
	}
	return []displayView{{Role: "assistant", Text: text}}
}

func toolCallSummary(name, arguments string) string {
	command, rows := toolCallArgs(arguments)
	if command != "" {
		return name + ": " + command
	}
	if len(rows) > 0 {
		return name + " " + rows[0].Key + "=" + previewText(rows[0].Value)
	}
	return name
}

func sessionTitle(meta store.SessionMeta) string {
	if meta.Name != "" {
		return meta.Name
	}
	return meta.ID.String()
}

func activeSet(events []store.Event, head uuid.UUID) map[uuid.UUID]bool {
	parents := make(map[uuid.UUID]uuid.UUID, len(events))
	for _, event := range events {
		parents[event.ID] = event.ParentID
	}
	active := map[uuid.UUID]bool{}
	for id := head; id != uuid.Nil && !active[id]; id = parents[id] {
		if _, ok := parents[id]; !ok {
			break
		}
		active[id] = true
	}
	return active
}

func childCounts(events []store.Event) map[uuid.UUID]int {
	children := make(map[uuid.UUID]int, len(events))
	for _, event := range events {
		if event.ParentID != uuid.Nil {
			children[event.ParentID]++
		}
	}
	return children
}

// branchDepths indents only where the timeline actually forks. A linear run of
// events shares one level, so a long conversation does not become a staircase.
func branchOutline(events []store.Event, views map[uuid.UUID]*eventView, head uuid.UUID) []*treeNode {
	nodes := make([]*treeNode, 0, 16)
	for _, event := range events {
		view := views[event.ID]
		if !view.ForkPoint && !view.Fork && event.ID != head {
			continue
		}
		nodes = append(nodes, &treeNode{Event: view})
	}
	return nodes
}

// turnIndex lists the user prompts, the natural checkpoints for jumping back
// into a long session.
func turnIndex(events []store.Event, views map[uuid.UUID]*eventView) []*treeNode {
	nodes := make([]*treeNode, 0, 16)
	for _, event := range events {
		if views[event.ID].Class == "user" {
			nodes = append(nodes, &treeNode{Event: views[event.ID]})
		}
	}
	return nodes
}

func toolCalls(events []store.Event, sessionIndex int) (map[string]string, map[string]string) {
	names, anchors := map[string]string{}, map[string]string{}
	for _, event := range events {
		if event.Record == nil || event.Record.Message == nil {
			continue
		}
		for i, call := range event.Record.Message.ToolCalls {
			names[call.ID] = string(call.Function.Name)
			anchors[call.ID] = fmt.Sprintf("s%d-call-%s-%d", sessionIndex, event.ID.String(), i)
		}
	}
	return names, anchors
}

func buildEvent(sessionIndex, index int, event store.Event, toolNames, toolAnchors map[string]string) *eventView {
	view := &eventView{
		Index: index, ID: event.ID.String(), DOMID: eventDOMID(sessionIndex, event.ID), ShortID: short(event.ID),
		Kind: string(event.Kind), Class: "event", Label: string(event.Kind), Icon: "·", BlobHash: event.BlobHash,
	}
	if event.ParentID != uuid.Nil {
		view.ParentID, view.ParentDOMID, view.ParentShort = event.ParentID.String(), eventDOMID(sessionIndex, event.ParentID), short(event.ParentID)
	}
	if event.Record == nil {
		view.Unresolved = true
		if preview := event.Preview; preview != nil {
			view.Class, view.Label, view.Icon = classFor(preview.Role)
			view.Text = markdownHTML(preview.Text)
		}
		view.Raw = pretty(event.Preview)
		return view
	}

	record := event.Record
	if !record.Timestamp.IsZero() {
		view.Time = record.Timestamp.UTC().Format(time.RFC3339)
	}
	view.Raw = pretty(record)
	switch {
	case record.Message != nil:
		message := record.Message
		view.Class, view.Label, view.Icon = classFor(message.Role)
		if message.Name != nil && *message.Name != "" {
			view.Label += " · " + *message.Name
		}
		view.Text, view.Reasoning = markdownHTML(message.Text()), markdownHTML(message.ReasoningContent)
		if message.Role == "tool" {
			view.ToolCallID, view.Status = message.ToolCallID, string(message.Status)
			view.CallLink = toolAnchors[message.ToolCallID]
			if name := toolNames[message.ToolCallID]; name != "" {
				view.Label = "tool result · " + name
			}
			view.Result = parseToolResult(message.Text())
		}
		for i, call := range message.ToolCalls {
			command, rows := toolCallArgs(call.Function.Arguments)
			view.ToolCalls = append(view.ToolCalls, toolCallView{
				ID: call.ID, Name: string(call.Function.Name), Args: prettyJSONString(call.Function.Arguments),
				Anchor:  fmt.Sprintf("s%d-call-%s-%d", sessionIndex, event.ID.String(), i),
				Command: command, ArgRows: rows,
			})
		}
		for _, image := range message.Images {
			view.Images = append(view.Images, imageOf(image.Name, image.MediaType, "", image.Data, image.Width, image.Height))
		}
		for _, artifact := range message.Artifacts {
			view.Images = append(view.Images, imageOf(artifact.Kind, artifact.MediaType, artifact.Prompt, artifact.Data, artifact.Width, artifact.Height))
		}
	case record.Compaction != nil:
		view.Class, view.Label, view.Icon = "compaction", "compaction", "≋"
		view.Summary = markdownHTML(record.Compaction.Summary)
		if record.Compaction.TaskChecklist != nil {
			view.Checklist = pretty(record.Compaction.TaskChecklist)
		}
	default:
		view.Class, view.Label, view.Icon = classFor(string(record.Kind))
		view.Text = markdownHTML(record.Text)
	}
	return view
}

// previewText is the one-line gist shown while an event is collapsed.
func previewText(source string) string {
	text := strings.Join(strings.Fields(source), " ")
	const limit = 160
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// toolCallArgs turns the recorded argument JSON into readable fields. The
// timeline already stores this; only the presentation changes.
func toolCallArgs(arguments string) (command string, rows []kv) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(arguments), &obj); err != nil {
		return "", nil
	}
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key == "command" {
			if text, ok := obj[key].(string); ok {
				command = text
				continue
			}
		}
		rows = append(rows, kv{key, toolValue(obj[key])})
	}
	return command, rows
}

// parseToolResult reads the {"exit_code":N,"output":"..."} shape the tools
// record, so the report can show output instead of an escaped JSON string.
func parseToolResult(text string) *toolResultView {
	var obj map[string]any
	if err := json.Unmarshal([]byte(text), &obj); err != nil {
		return nil
	}
	output, ok := obj["output"].(string)
	if !ok {
		return nil
	}
	result := &toolResultView{Output: output}
	if code, ok := obj["exit_code"].(float64); ok {
		result.ExitCode = fmt.Sprint(int(code))
		result.Failed = code != 0
	}
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key == "output" || key == "exit_code" {
			continue
		}
		result.Rows = append(result.Rows, kv{key, toolValue(obj[key])})
	}
	return result
}

func toolValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

func classFor(role string) (class, label, icon string) {
	switch role {
	case "user":
		return "user", role, "▸"
	case "assistant":
		return "assistant", role, "◆"
	case "tool":
		return "tool", role, "⚙"
	case "system":
		return "system", role, "■"
	case "compaction":
		return "compaction", role, "≋"
	case string(store.KindSidecarLoopResponse):
		return "sidecar", role, "↻"
	default:
		return "event", role, "·"
	}
}

func markdownHTML(source string) template.HTML {
	if source == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(source), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(source))
	}
	return template.HTML(sanitizer.SanitizeBytes(buf.Bytes()))
}

func imageOf(name, mediaType, prompt string, data []byte, width, height int) imageView {
	view := imageView{Name: name, MediaType: mediaType, Prompt: prompt, Size: len(data), Width: width, Height: height}
	if len(data) > 0 && safeImageTypes[mediaType] {
		view.Src = template.URL("data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data))
	}
	return view
}

func usageOf(meta *store.LastTurnMeta) usageProgressView {
	if meta == nil {
		return usageProgressView{PromptLabel: "0 prompt / 0 context", CompletionLabel: "0 completion", WindowLabel: "0"}
	}
	u := meta.Usage
	view := usageProgressView{
		Present: true, Prompt: u.PromptTokens, Completion: u.CompletionTokens, Total: u.TotalTokens, Window: meta.ContextWindow,
		PromptLabel:     fmt.Sprintf("%d prompt / %d context", u.PromptTokens, meta.ContextWindow),
		CompletionLabel: fmt.Sprintf("%d completion", u.CompletionTokens), WindowLabel: fmt.Sprint(meta.ContextWindow),
	}
	if u.PromptTokensDetails != nil {
		view.Cached = u.PromptTokensDetails.CachedTokens
		view.CacheWrite = u.PromptTokensDetails.CacheWriteTokens
	}
	if u.CompletionTokensDetails != nil {
		view.Reasoning = u.CompletionTokensDetails.ReasoningTokens
	}
	if meta.ContextWindow > 0 {
		view.PromptPercent = cappedPercent(u.PromptTokens, meta.ContextWindow)
		view.CompletionPercent = cappedPercent(u.CompletionTokens, meta.ContextWindow)
		view.Overflow = u.PromptTokens > meta.ContextWindow
	}
	return view
}

func cappedPercent(value, total uint64) float64 {
	if total == 0 {
		return 0
	}
	percent := float64(value) / float64(total) * 100
	if percent > 100 {
		return 100
	}
	return percent
}

func metaRows(meta store.SessionMeta, head uuid.UUID) []kv {
	rows := []kv{
		{"session id", meta.ID.String()}, {"name", meta.Name}, {"agent kind", string(meta.AgentKind)},
		{"provider / model", meta.Provider + " / " + meta.Model}, {"cwd", meta.Cwd},
		{"created", formatTime(meta.CreatedAt)}, {"updated", formatTime(meta.UpdatedAt)}, {"head event", head.String()},
	}
	if meta.ParentID != uuid.Nil {
		rows = append(rows, kv{"parent session", meta.ParentID.String()})
	}
	for _, id := range meta.Offsprings {
		rows = append(rows, kv{"offspring session", id.String()})
	}
	if meta.MaxTurns != nil {
		rows = append(rows, kv{"max turns", fmt.Sprint(*meta.MaxTurns)})
	}
	if meta.EvalBashScript != nil {
		rows = append(rows, kv{"eval bash script", *meta.EvalBashScript})
	}
	if c := meta.Conclusion; c != nil {
		rows = append(rows, kv{"conclusion", strings.TrimSpace(string(c.Status) + " " + c.Reason)})
	}
	if t := meta.LastTurnMeta; t != nil {
		rows = append(rows, kv{"last turn model", t.Provider + " / " + t.Model})
	}
	return rows
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func eventDOMID(sessionIndex int, id uuid.UUID) string {
	return fmt.Sprintf("s%d-e-%s", sessionIndex, id.String())
}

func short(id uuid.UUID) string {
	s := id.String()
	return s[len(s)-8:]
}

func pretty(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("<unencodable: %v>", err)
	}
	return string(data)
}

func prettyJSONString(s string) string {
	var out bytes.Buffer
	if json.Valid([]byte(s)) && json.Indent(&out, []byte(s), "", "  ") == nil {
		return out.String()
	}
	return s
}
