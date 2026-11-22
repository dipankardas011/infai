package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/browser"
	"github.com/dipankardas011/infai/pkg/agent/browserext"
)

// ChromeProfile is one local Chrome profile: its directory under the user data
// dir (e.g. "Default", "Profile 1") and the name shown in Chrome's UI.
type ChromeProfile struct {
	Dir  string
	Name string
}

// chromeUserDataDirs lists the browser user-data dirs we look in, most likely
// first. A machine may have Chrome, Chromium, or both.
func chromeUserDataDirs() ([]string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("locate user config dir: %w", err)
	}
	return []string{
		filepath.Join(configDir, "google-chrome"),
		filepath.Join(configDir, "chromium"),
	}, nil
}

// ListChromeProfiles reads the profiles of the first installed browser. No
// browser, or one that never ran, yields an empty list rather than an error.
func ListChromeProfiles() ([]ChromeProfile, error) {
	dirs, err := chromeUserDataDirs()
	if err != nil {
		return nil, err
	}
	var statePath string
	var raw []byte
	for _, dir := range dirs {
		raw, err = os.ReadFile(filepath.Join(dir, "Local State"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filepath.Join(dir, "Local State"), err)
		}
		statePath = filepath.Join(dir, "Local State")
		break
	}
	if statePath == "" {
		return []ChromeProfile{}, nil
	}
	var state struct {
		Profile struct {
			InfoCache map[string]struct {
				Name string `json:"name"`
			} `json:"info_cache"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("parse %s: %w", statePath, err)
	}
	profiles := make([]ChromeProfile, 0, len(state.Profile.InfoCache))
	for dir, info := range state.Profile.InfoCache {
		profiles = append(profiles, ChromeProfile{Dir: dir, Name: info.Name})
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Dir < profiles[j].Dir })
	return profiles, nil
}

// RunBrowserStatus shows whether the extension is connected, which Chrome
// profile holds it, which tabs sessions drive, and what to do when something is
// missing. The local half still prints when the daemon is down, because that is
// exactly when you need to know whether the extension is installed.
func RunBrowserStatus(ctx context.Context, client *RemoteClient, out io.Writer, extDir string) error {
	status, statusErr := client.BrowserStatus(ctx)
	if statusErr != nil {
		status = browser.Status{}
	}

	now := time.Now()
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	state := "not connected"
	switch {
	case statusErr != nil:
		state = "unknown (daemon unreachable)"
	case status.Connected:
		state = "connected"
	}
	fmt.Fprintf(tw, "extension:\t%s\n", state)
	fmt.Fprintf(tw, "daemon:\t%s\n", client.BaseURL())
	fmt.Fprintf(tw, "profile:\t%s\n", orDash(status.Profile))
	fmt.Fprintf(tw, "version:\t%s\n", orDash(status.ExtensionVersion))
	since := "-"
	if !status.ConnectedAt.IsZero() {
		since = formatBrowserTime(status.ConnectedAt, now)
	}
	fmt.Fprintf(tw, "connected since:\t%s\n", since)
	if err := tw.Flush(); err != nil {
		return err
	}
	if statusErr != nil {
		fmt.Fprintf(out, "daemon error:\t%v\n", statusErr)
	}

	fmt.Fprintln(out, "\nSessions:")
	switch {
	case statusErr != nil:
		fmt.Fprintln(out, "  unavailable while the daemon is unreachable")
	case len(status.Sessions) == 0:
		fmt.Fprintln(out, "  none")
	default:
		tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  NAME\tTAB\tLAST ACTION\tURL\tLAST USED")
		for _, s := range status.Sessions {
			name := s.SessionName
			if name == "" {
				name = s.SessionID.String()
			}
			lastUsed := "-"
			if !s.LastUsed.IsZero() {
				lastUsed = formatBrowserTime(s.LastUsed, now)
			}
			fmt.Fprintf(tw, "  %s\t%d\t%s\t%s\t%s\n", name, s.TabID, orDash(s.LastAction), orDash(s.URL), lastUsed)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	return printChromeProfiles(out, status, extDir, statusErr != nil)
}

func printChromeProfiles(out io.Writer, status browser.Status, extDir string, daemonDown bool) error {
	fmt.Fprintln(out, "\nChrome profiles:")

	extID, idErr := browserext.ExtensionID(extDir)
	written := false
	if idErr != nil {
		fmt.Fprintf(out, "  extension folder: %s (cannot determine id: %v)\n", extDir, idErr)
		extID = ""
	} else {
		_, statErr := os.Stat(filepath.Join(extDir, "manifest.json"))
		written = statErr == nil
		fmt.Fprintf(out, "  extension folder: %s\n", extDir)
		fmt.Fprintf(out, "  extension id:     %s\n", extID)
		if !written {
			fmt.Fprintln(out, "  not written yet: start `infaiw server` to install it")
		}
	}

	profiles, err := ListChromeProfiles()
	if err != nil {
		fmt.Fprintf(out, "  unavailable: %v\n", err)
		return nil
	}
	if len(profiles) == 0 {
		fmt.Fprintln(out, "  none found")
		return nil
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	installedIn := 0
	for _, p := range profiles {
		installed := written && profileHasExtension(p.Dir, extID)
		if installed {
			installedIn++
		}
		var marks []string
		if written {
			if installed {
				marks = append(marks, "installed")
			} else {
				marks = append(marks, "not installed")
			}
		}
		if status.Connected && status.Profile != "" && p.Name == status.Profile {
			marks = append(marks, "connected")
		}
		fmt.Fprintf(tw, "  %s\t(%s)\t%s\n", p.Name, p.Dir, strings.Join(marks, ", "))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if installedIn > 1 {
		fmt.Fprintln(out, "\n  warn: installed in more than one profile; enable it in one only, or the")
		fmt.Fprintln(out, "        profiles will keep replacing each other's connection.")
	}
	printBrowserNextSteps(out, status, written, installedIn, extDir, daemonDown)
	return nil
}

// printBrowserNextSteps says only what is still missing, so a healthy setup
// prints nothing.
func printBrowserNextSteps(out io.Writer, status browser.Status, written bool, installedIn int, extDir string, daemonDown bool) {
	if status.Connected {
		return
	}
	var steps []string
	if !written {
		steps = append(steps, "start the daemon: infaiw server   (it writes and pairs the extension)")
	} else if daemonDown {
		steps = append(steps, "start the daemon: infaiw server   (the extension needs it to connect)")
	}
	if written && installedIn == 0 {
		steps = append(steps,
			"in Chrome: chrome://extensions -> Developer mode -> Load unpacked -> "+extDir)
	}
	if installedIn > 0 {
		steps = append(steps, "open Chrome in that profile and click Reload on the extension")
	}
	steps = append(steps, "then check the extension's side panel: it shows the connection and lets you label the profile")

	fmt.Fprintln(out, "\nWhat to do:")
	for _, s := range steps {
		fmt.Fprintf(out, "  - %s\n", s)
	}
}

// profileHasExtension reports whether Chrome's records for one profile list
// extID. Chrome rewrites Preferences asynchronously, so a profile can lag a
// fresh install by a few seconds.
func profileHasExtension(profileDir, extID string) bool {
	if extID == "" {
		return false
	}
	dirs, err := chromeUserDataDirs()
	if err != nil {
		return false
	}
	for _, dir := range dirs {
		raw, err := os.ReadFile(filepath.Join(dir, profileDir, "Preferences"))
		if err != nil {
			continue
		}
		var prefs struct {
			Extensions struct {
				Settings map[string]json.RawMessage `json:"settings"`
			} `json:"extensions"`
		}
		if json.Unmarshal(raw, &prefs) != nil {
			continue
		}
		if _, ok := prefs.Extensions.Settings[extID]; ok {
			return true
		}
	}
	return false
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
