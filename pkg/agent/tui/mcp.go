package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/glue"
	"github.com/dipankardas011/infai/pkg/agent/store"
)

// MCPManagementClient is the part of the agent client the mcp commands use.
type MCPManagementClient interface {
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

// errMCPFormCanceled is what the form returns when the operator leaves it
// without saving. Callers report it as "nothing was saved", not as a failure.
var errMCPFormCanceled = errors.New("MCP server form canceled")

// RunMCPList prints every configured server and the state of its authorization.
func RunMCPList(ctx context.Context, client MCPManagementClient, out io.Writer) error {
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
	if root, err := store.Root(); err == nil {
		fmt.Fprintln(out, styles.muted.Render("config: "+filepath.Join(root, "tools.json")))
	}
	return nil
}

// RunMCPAdd opens the form for one new server, stores it, authorizes it when the
// operator chose OAuth, and reports what a connection finds.
func RunMCPAdd(ctx context.Context, client MCPManagementClient, in io.Reader, out io.Writer) error {
	servers, err := client.ListMCPServers(ctx)
	if err != nil {
		return err
	}
	taken := make(map[string]bool, len(servers))
	for _, server := range servers {
		taken[server.Name] = true
	}

	name, config, err := runMCPForm(ctx, in, out, nil, taken)
	if err != nil {
		if errors.Is(err, errMCPFormCanceled) {
			fmt.Fprintln(out, newHarnessStyles().muted.Render("Nothing was saved."))
			return nil
		}
		return err
	}
	if _, err := client.AddMCPServer(ctx, glue.MCPServerInput{Name: name, Config: config}); err != nil {
		return err
	}
	fmt.Fprintln(out, newHarnessStyles().active.Render("Saved "+name))

	if config.OAuth != nil {
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
func RunMCPEdit(ctx context.Context, client MCPManagementClient, in io.Reader, out io.Writer, name string) error {
	servers, err := client.ListMCPServers(ctx)
	if err != nil {
		return err
	}
	view, err := pickMCPServer(ctx, in, out, servers, name, "MCP server edit", "Select the server to edit")
	if err != nil {
		return err
	}

	name, config, err := runMCPForm(ctx, in, out, &view, nil)
	if err != nil {
		if errors.Is(err, errMCPFormCanceled) {
			fmt.Fprintln(out, newHarnessStyles().muted.Render("Nothing was saved."))
			return nil
		}
		return err
	}
	if _, err := client.EditMCPServer(ctx, glue.MCPServerInput{Name: name, Config: config}); err != nil {
		return err
	}
	styles := newHarnessStyles()
	fmt.Fprintln(out, styles.active.Render("Saved "+name))
	if view.Auth == contracts.MCPAuthOAuth {
		fmt.Fprintln(out, styles.muted.Render("run `infaiw mcp refresh "+name+"` to authorize again if the client details changed"))
	}
	return probeMCP(ctx, client, out, name)
}

// RunMCPRemove deletes a server and its stored authorization, after a
// confirmation.
func RunMCPRemove(ctx context.Context, client MCPManagementClient, in io.Reader, out io.Writer, name string) error {
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
func RunMCPRefresh(ctx context.Context, client MCPManagementClient, in io.Reader, out io.Writer, name string) error {
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
	view, err := pickMCPServer(ctx, in, out, oauthServers, name, "MCP authorization", "Select the server to authorize")
	if err != nil {
		return err
	}
	return authorizeMCP(ctx, client, out, view.Name)
}

// authorizeMCP runs one login and prints the URL the operator has to open.
func authorizeMCP(ctx context.Context, client MCPManagementClient, out io.Writer, name string) error {
	styles := newHarnessStyles()

	showedCallback := false
	err := client.LoginMCPServer(ctx, name, func(update glue.MCPLoginOutput) {
		if update.Callback != "" && !showedCallback {
			showedCallback = true
			// The authorization server refuses a request by naming the callback
			// it was given, so the operator can read the two against each other.
			fmt.Fprintln(out, styles.muted.Render("callback "+update.Callback))
		}
		if update.URL == "" {
			return
		}
		fmt.Fprintln(out, styles.screenBody.Render("Open this URL to authorize infaiw:"))
		fmt.Fprintln(out, styles.active.Render(update.URL))
		openBrowser(update.URL)
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(out, styles.active.Render("Authorized "+name))
	fmt.Fprintln(out, styles.muted.Render(mcpStaleSessionNote))
	return nil
}

// openBrowser hands a URL to the desktop's own opener. It is best effort: the
// URL is printed either way, because the agent server may be running where this
// client cannot open a browser, and a machine without an opener is not a failure.
func openBrowser(url string) {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	if command.Start() != nil {
		return
	}
	_ = command.Process.Release()
}

// probeMCP reports what a connection to one server finds.
func probeMCP(ctx context.Context, client MCPManagementClient, out io.Writer, name string) error {
	probe, err := client.ProbeMCPServer(ctx, name)
	if err != nil {
		return err
	}
	styles := newHarnessStyles()
	if probe.Tools+probe.Prompts+probe.Resources == 0 {
		// Nothing came back. Why is in the server log: a server that cannot be
		// reached is skipped and logged there, never here.
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
	if view.Transport == "stdio" {
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
