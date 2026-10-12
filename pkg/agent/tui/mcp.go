package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/glue"
)

// mcpManagementClient is the part of the agent client the mcp commands use.
type mcpManagementClient interface {
	ListMCPServers(context.Context) ([]glue.MCPServerView, error)
	AddMCPServer(context.Context, glue.MCPServerInput) (glue.MCPServerView, error)
	EditMCPServer(context.Context, glue.MCPServerInput) (glue.MCPServerView, error)
	RemoveMCPServer(context.Context, string) error
	ProbeMCPServer(context.Context, string) (glue.MCPProbeOutput, error)
	LoginMCPServer(context.Context, string, func(glue.MCPLoginOutput)) error
}

// mcpStaleSessionNote is printed whenever a credential changes: a session holds
// its connections and its token from the moment it was created.
const mcpStaleSessionNote = "a session that was already using this server keeps its old credential — close and reopen it for the change to take effect"

// noticeMCPStatus tells the operator which configured MCP servers this session
// could not use. A session that is short of tools says so, with the reason and
// the remedy the engine recorded, instead of leaving it to the agent's log.
func (m *chatModel) noticeMCPStatus(status []contracts.MCPServerStatus) {
	if notice := mcpUnavailableNotice(status); notice != "" {
		m.showNotice("MCP servers unavailable", notice, false)
	}
}

// mcpUnavailableNotice is the body of that notice, one line per server.
func mcpUnavailableNotice(status []contracts.MCPServerStatus) string {
	var lines []string
	for _, server := range status {
		if server.Reason == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", server.Server, server.Reason))
	}
	return strings.Join(lines, "\n")
}

// errMCPFormCanceled is what the form returns when the operator leaves it
// without saving. Callers report it as "nothing was saved", not as a failure.
var errMCPFormCanceled = errors.New("MCP server form canceled")

// RunMCPList prints every configured server and the state of its authorization.
func RunMCPList(ctx context.Context, client mcpManagementClient, out io.Writer) error {
	servers, err := client.ListMCPServers(ctx)
	if err != nil {
		return err
	}

	styles := newHarnessStyles()
	fmt.Fprintln(out, styles.heading("MCP servers", everforest.Background))
	if len(servers) == 0 {
		fmt.Fprintln(out, styles.screenBody.Render("No MCP servers configured."))
	}
	now := time.Now()
	for _, server := range servers {
		fmt.Fprintln(out, styles.screenRow.Render(mcpRow(server, now)))
	}
	return nil
}

// RunMCPAdd opens the form for one new server, stores it, authorizes it when the
// operator chose OAuth, and reports what a connection finds.
func RunMCPAdd(ctx context.Context, client mcpManagementClient, in io.Reader, out io.Writer) error {
	servers, err := client.ListMCPServers(ctx)
	if err != nil {
		return err
	}
	taken := make(map[string]bool, len(servers))
	for _, server := range servers {
		taken[server.Name] = true
	}

	input, err := runMCPForm(ctx, in, out, nil, taken)
	if err != nil {
		if errors.Is(err, errMCPFormCanceled) {
			fmt.Fprintln(out, newHarnessStyles().muted.Render("Nothing was saved."))
			return nil
		}
		return err
	}
	if _, err := client.AddMCPServer(ctx, input); err != nil {
		return err
	}
	name := input.Name
	fmt.Fprintln(out, newHarnessStyles().active.Render("Saved "+name))

	if input.Config.OAuth != nil {
		if err := authorizeMCP(ctx, client, out, name); err != nil {
			return err
		}
	}
	return probeMCP(ctx, client, out, name)
}

// RunMCPEdit replaces the values of an existing server in the same form. The
// transport and the authentication kind are fixed: a different kind is a
// different server, so remove it and add it again. A credential left empty keeps
// the stored one, which a client cannot read back.
func RunMCPEdit(ctx context.Context, client mcpManagementClient, in io.Reader, out io.Writer, name string) error {
	servers, err := client.ListMCPServers(ctx)
	if err != nil {
		return err
	}
	view, err := pickMCPServer(ctx, in, out, servers, name, "MCP server edit", "Select the server to edit")
	if err != nil {
		return err
	}

	input, err := runMCPForm(ctx, in, out, &view, nil)
	if err != nil {
		if errors.Is(err, errMCPFormCanceled) {
			fmt.Fprintln(out, newHarnessStyles().muted.Render("Nothing was saved."))
			return nil
		}
		return err
	}
	if _, err := client.EditMCPServer(ctx, input); err != nil {
		return err
	}
	name = input.Name
	styles := newHarnessStyles()
	fmt.Fprintln(out, styles.active.Render("Saved "+name))
	if view.Auth == contracts.MCPAuthOAuth {
		fmt.Fprintln(out, styles.muted.Render("run `infaiw mcp refresh "+name+"` to authorize again if the client details changed"))
	}
	return probeMCP(ctx, client, out, name)
}

// RunMCPRemove deletes a server and its stored authorization, after a
// confirmation.
func RunMCPRemove(ctx context.Context, client mcpManagementClient, in io.Reader, out io.Writer, name string) error {
	servers, err := client.ListMCPServers(ctx)
	if err != nil {
		return err
	}
	view, err := pickMCPServer(ctx, in, out, servers, name, "MCP server removal", "Select the server to remove")
	if err != nil {
		return err
	}

	styles := newHarnessStyles()
	confirm := fmt.Sprintf("Remove %s and its stored authorization", view.Name)
	choice, err := runProviderMenu(ctx, in, out, "MCP server removal", confirm, []string{"Keep it", "Remove it"})
	if err != nil {
		if errors.Is(err, errSelectionCanceled) {
			fmt.Fprintln(out, styles.muted.Render("Nothing was removed."))
			return nil
		}
		return err
	}
	if choice != "Remove it" {
		fmt.Fprintln(out, styles.muted.Render("Nothing was removed."))
		return nil
	}
	if err := client.RemoveMCPServer(ctx, view.Name); err != nil {
		return err
	}
	fmt.Fprintln(out, styles.active.Render("Removed "+view.Name))
	return nil
}

// RunMCPRefresh re-runs the authorization for one OAuth server.
func RunMCPRefresh(ctx context.Context, client mcpManagementClient, in io.Reader, out io.Writer, name string) error {
	servers, err := client.ListMCPServers(ctx)
	if err != nil {
		return err
	}
	var oauthServers []glue.MCPServerView
	for _, server := range servers {
		if server.Auth == contracts.MCPAuthOAuth {
			oauthServers = append(oauthServers, server)
		}
	}
	if len(oauthServers) == 0 {
		return errors.New("no configured MCP server uses OAuth; set OAuth for one with `infaiw mcp edit`")
	}
	if name != "" && !slices.ContainsFunc(oauthServers, func(server glue.MCPServerView) bool { return server.Name == name }) {
		return fmt.Errorf("MCP server %q does not use OAuth; set OAuth for it with `infaiw mcp edit`", name)
	}
	view, err := pickMCPServer(ctx, in, out, oauthServers, name, "MCP authorization", "Select the server to authorize")
	if err != nil {
		return err
	}
	return authorizeMCP(ctx, client, out, view.Name)
}

// authorizeMCP runs one login and prints the URL the operator has to open.
func authorizeMCP(ctx context.Context, client mcpManagementClient, out io.Writer, name string) error {
	styles := newHarnessStyles()

	shown, opened := false, false
	err := client.LoginMCPServer(ctx, name, func(update glue.MCPLoginOutput) {
		if update.Callback != "" && !shown {
			shown = true
			// The authorization server refuses a request by naming the callback
			// it was given, so the operator can read the two against each other.
			fmt.Fprintln(out, styles.muted.Render("callback "+update.Callback))
		}
		if update.URL == "" || opened {
			// The URL is printed and opened once: a client that reports it again
			// would otherwise open a second tab per update.
			return
		}
		opened = true
		fmt.Fprintln(out, styles.screenBody.Render("Open this URL to authorize infaiw:"))
		fmt.Fprintln(out, styles.active.Render(update.URL))
		if err := openBrowser(update.URL); err != nil {
			fmt.Fprintln(out, styles.muted.Render(err.Error()+" — open the URL above by hand"))
		}
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(out, styles.active.Render("Authorized "+name))
	fmt.Fprintln(out, styles.muted.Render(mcpStaleSessionNote))
	return nil
}

// The desktop openers this client knows, by platform.
const (
	openerDarwin  = "open"
	openerWindows = "rundll32"
	openerLinux   = "xdg-open"
	windowsOpen   = "url.dll,FileProtocolHandler"
)

// openBrowser hands an authorization URL to the desktop's own opener. It is best
// effort and never fatal: the URL is printed either way, because the agent server
// may be running where this client cannot open a browser. Only an http(s) URL is
// opened, and it is passed as an argument, not as a flag the opener would read.
func openBrowser(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return errors.New("the authorization URL is not a URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("refusing to open a %q URL", parsed.Scheme)
	}
	var argv []string
	switch runtime.GOOS {
	case "darwin":
		argv = []string{openerDarwin, rawURL}
	case "windows":
		argv = []string{openerWindows, windowsOpen, rawURL}
	default:
		argv = []string{openerLinux, rawURL}
	}
	command := exec.Command(argv[0], argv[1:]...)
	if err := command.Start(); err != nil {
		return fmt.Errorf("no desktop opener is available")
	}
	return command.Process.Release()
}

// probeMCP reports what a connection to one server finds. A server that could
// not be reached is a failure of the command, not a server without tools.
func probeMCP(ctx context.Context, client mcpManagementClient, out io.Writer, name string) error {
	probe, err := client.ProbeMCPServer(ctx, name)
	if err != nil {
		return err
	}
	styles := newHarnessStyles()
	if probe.Reason != "" {
		return fmt.Errorf("%s could not be reached: %s", probe.Server, probe.Reason)
	}
	if probe.Tools+probe.Prompts+probe.Resources == 0 {
		fmt.Fprintln(out, styles.muted.Render(probe.Server+" offered no tools, prompts or resources"))
		return nil
	}
	fmt.Fprintln(out, styles.active.Render(fmt.Sprintf("%s: %d tools, %d prompts, %d resources", probe.Server, probe.Tools, probe.Prompts, probe.Resources)))
	return nil
}

// pickMCPServer takes the named server, or asks which one when the operator did
// not name it.
func pickMCPServer(ctx context.Context, in io.Reader, out io.Writer, servers []glue.MCPServerView, name, title, body string) (glue.MCPServerView, error) {
	if len(servers) == 0 {
		return glue.MCPServerView{}, errors.New("no MCP servers are configured")
	}
	if name != "" {
		for _, server := range servers {
			if server.Name == name {
				return server, nil
			}
		}
		return glue.MCPServerView{}, fmt.Errorf("MCP server %q is not configured", name)
	}

	options := make([]string, 0, len(servers))
	for _, server := range servers {
		options = append(options, server.Name)
	}
	choice, err := runProviderMenu(ctx, in, out, title, body, options)
	if err != nil {
		return glue.MCPServerView{}, err
	}
	for _, server := range servers {
		if server.Name == choice {
			return server, nil
		}
	}
	return glue.MCPServerView{}, fmt.Errorf("MCP server %q is not configured", choice)
}

// mcpRow is one server's line: name, transport, authentication, endpoint and
// token state. It carries no credential — the view it renders has none.
func mcpRow(view glue.MCPServerView, now time.Time) string {
	return fmt.Sprintf("%-22s  %-16s  %-11s  %-40s  %s", view.Name, view.Transport, view.Auth, mcpTarget(view), formatTokenState(view, now))
}

// mcpTarget is what a row calls the server's endpoint: the URL of a
// streamable-http server, or the command line of a stdio one.
func mcpTarget(view glue.MCPServerView) string {
	if view.Transport == contracts.MCPTransportStdio {
		return strings.Join(append([]string{view.Command}, view.Args...), " ")
	}
	return view.URL
}

// formatTokenState describes a server's stored authorization: nothing for a
// server that does not use OAuth, and otherwise whether a token exists, when it
// expires.
func formatTokenState(view glue.MCPServerView, now time.Time) string {
	if view.Auth != contracts.MCPAuthOAuth {
		return "-"
	}
	if !view.HasToken {
		return "no token"
	}
	if view.TokenExpiry == nil {
		return "no expiry"
	}
	left := view.TokenExpiry.Sub(now)
	if left <= 0 {
		return "expired"
	}
	return fmt.Sprintf("%s (%s left)", view.TokenExpiry.UTC().Format("2006-01-02 15:04Z"), formatTokenLeft(left))
}

// formatTokenLeft is a token's remaining life at minute resolution, without the
// zero seconds a bare Duration prints.
func formatTokenLeft(left time.Duration) string {
	switch {
	case left >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(left.Hours()), int(left.Minutes())%60)
	case left >= time.Minute:
		return fmt.Sprintf("%dm", int(left.Minutes()))
	default:
		return "under a minute"
	}
}
