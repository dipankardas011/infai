package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/mod/semver"
)

const releaseAPIURL = "https://api.github.com/repos/dipankardas011/infai/releases/latest"

// updateCheckMsg reports the result of the async release check.
// latest is empty when the app is current or the check failed.
type updateCheckMsg struct{ latest string }

var updateHTTPClient = &http.Client{Timeout: 5 * time.Second}

// normalizeVersion gives a semver-comparable form ("v1.2.3") or "" if invalid.
func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return ""
	}
	return v
}

// isNewerVersion reports whether latest is a strictly newer release than
// current. Dev/invalid versions never trigger an update notice.
func isNewerVersion(current, latest string) bool {
	c, l := normalizeVersion(current), normalizeVersion(latest)
	if c == "" || l == "" {
		return false
	}
	return semver.Compare(l, c) > 0
}

// checkForUpdateCmd fetches the latest release tag in the background.
// Bubble Tea runs the returned function in its own goroutine, so startup is
// never blocked; the result arrives later as an updateCheckMsg. Any error
// (offline, rate limit, dev build) resolves to "no update" silently.
func checkForUpdateCmd(current string) tea.Cmd {
	if normalizeVersion(current) == "" {
		return nil // dev build — nothing meaningful to compare against
	}
	return func() tea.Msg {
		req, err := http.NewRequest(http.MethodGet, releaseAPIURL, nil)
		if err != nil {
			return updateCheckMsg{}
		}
		req.Header.Set("User-Agent", "infai-update-check")
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := updateHTTPClient.Do(req)
		if err != nil {
			return updateCheckMsg{}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return updateCheckMsg{}
		}
		var release struct {
			TagName string `json:"tag_name"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
			return updateCheckMsg{}
		}
		latest := normalizeVersion(release.TagName)
		if latest == "" {
			return updateCheckMsg{}
		}
		if isNewerVersion(current, release.TagName) {
			return updateCheckMsg{latest: latest}
		}
		return updateCheckMsg{}
	}
}

// renderUpdateNotice draws the centered "update available" popup.
func renderUpdateNotice(current, latest string, width, height int) string {
	t := ActiveTheme
	title := lipgloss.NewStyle().Foreground(t.Warning).Bold(true)
	label := lipgloss.NewStyle().Foreground(t.Muted).Width(9).Align(lipgloss.Right)
	cur := lipgloss.NewStyle().Foreground(t.Text)
	new := lipgloss.NewStyle().Foreground(t.Success).Bold(true)
	dim := styleMuted

	if c := normalizeVersion(current); c != "" {
		current = c
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		title.Render("Update available")+dim.Render("  —  a newer infai release is out"),
		"",
		label.Render("current")+"  "+cur.Render(current),
		label.Render("latest")+"  "+new.Render(latest),
		"",
		dim.Render("brew upgrade dipankardas011/tap/infai"),
		dim.Render("or download from github.com/dipankardas011/infai/releases"),
		"",
		styleKey.Render("enter")+dim.Render(": continue"),
	)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Warning).
		Padding(1, 3).
		MaxHeight(max(height, 1)).
		Render(content)

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

func updateToastText(latest string) string {
	return fmt.Sprintf("update available: %s — see github releases", latest)
}
