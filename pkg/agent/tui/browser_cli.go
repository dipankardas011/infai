package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/browser"
)

// RunBrowserStatus shows the connected browsers, which tabs sessions drive, and
// what to do when nothing is connected. It still prints when the daemon is
// down, because that is exactly when you need the setup steps.
func RunBrowserStatus(ctx context.Context, client *RemoteClient, out io.Writer, extDir string) error {
	status, statusErr := client.BrowserStatus(ctx)
	if statusErr != nil {
		status = browser.Status{}
	}

	now := time.Now()
	fmt.Fprintf(out, "daemon:  %s\n", client.BaseURL())
	if statusErr != nil {
		fmt.Fprintf(out, "daemon error:  %v\n", statusErr)
	}

	fmt.Fprintln(out, "\nBrowsers:")
	switch {
	case statusErr != nil:
		fmt.Fprintln(out, "  unknown while the daemon is unreachable")
	case len(status.Clients) == 0:
		fmt.Fprintln(out, "  none connected")
	default:
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  ID\tTABS\tVERSION\tCONNECTED")
		for _, c := range status.Clients {
			since := "-"
			if !c.ConnectedAt.IsZero() {
				since = formatBrowserTime(c.ConnectedAt, now)
			}
			fmt.Fprintf(tw, "  %s\t%d\t%s\t%s\n", c.ID, c.Tabs, orDash(c.ExtensionVersion), since)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	fmt.Fprintln(out, "\nSessions:")
	switch {
	case statusErr != nil:
		fmt.Fprintln(out, "  unavailable while the daemon is unreachable")
	case len(status.Sessions) == 0:
		fmt.Fprintln(out, "  none")
	default:
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  NAME\tBROWSER\tTAB\tLAST ACTION\tURL\tLAST USED")
		for _, s := range status.Sessions {
			name := s.SessionName
			if name == "" {
				name = s.SessionID.String()
			}
			lastUsed := "-"
			if !s.LastUsed.IsZero() {
				lastUsed = formatBrowserTime(s.LastUsed, now)
			}
			fmt.Fprintf(tw, "  %s\t%s\t%d\t%s\t%s\t%s\n", name, orDash(s.Client), s.TabID, orDash(s.LastAction), orDash(s.URL), lastUsed)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	if statusErr == nil && len(status.Clients) > 0 {
		return nil
	}
	printBrowserNextSteps(out, extDir, statusErr != nil)
	return nil
}

// printBrowserNextSteps is shown only while no browser is connected.
func printBrowserNextSteps(out io.Writer, extDir string, daemonDown bool) {
	_, statErr := os.Stat(filepath.Join(extDir, "manifest.json"))
	written := statErr == nil

	var steps []string
	switch {
	case !written:
		steps = append(steps, "start the daemon: infaiw server   (it writes and pairs the extension)")
	case daemonDown:
		steps = append(steps, "start the daemon: infaiw server   (the extension needs it to connect)")
	}
	steps = append(steps,
		"load the extension: chrome://extensions (or brave://, edge://) -> Developer mode -> Load unpacked -> "+extDir,
		"already loaded? click Reload on it after a daemon restart",
		"name each profile in the extension's side panel; its id becomes <browser>__<name> (a -<hex> suffix is added when two profiles share a name)")

	fmt.Fprintln(out, "\nWhat to do:")
	for _, s := range steps {
		fmt.Fprintf(out, "  - %s\n", s)
	}
}

func formatBrowserTime(t, now time.Time) string {
	ago := now.Sub(t).Truncate(time.Second)
	if ago < 0 {
		ago = 0
	}
	return fmt.Sprintf("%s ago (%s)", ago, t.Local().Format(time.RFC3339))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
