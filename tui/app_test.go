package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dipankardas011/infai/db"
)

func newTestApp(t *testing.T) *AppModel {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	database, err := db.Open()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(database.Close)
	app := NewApp(database, nil, nil, 80, 24)
	return &app
}

func keyRune(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// Typing into the profiles filter must not trigger global shortcuts like
// 't' (theme selector), 'q' (quit), or '2' (tab switch).
func TestGlobalKeysSuppressedWhileFiltering(t *testing.T) {
	app := newTestApp(t)

	// Enter filter mode on the Profiles tab.
	app.Update(keyRune('/'))
	if !app.home.profilesTab.IsFiltering() {
		t.Fatal("expected profiles list to be filtering after '/'")
	}

	if _, cmd := app.Update(keyRune('q')); cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("'q' while filtering must not quit the app")
		}
	}
	app.Update(keyRune('t'))
	if app.screen != screenHome {
		t.Fatalf("'t' while filtering must not open theme selector, got screen %d", app.screen)
	}
	app.Update(keyRune('2'))
	if app.home.activeTab != tabProfiles {
		t.Fatal("'2' while filtering must not switch tabs")
	}
}

// The same keys must keep working when no input is capturing keystrokes.
func TestGlobalKeysActiveWhenNotFiltering(t *testing.T) {
	app := newTestApp(t)

	app.Update(keyRune('2'))
	if app.home.activeTab != tabRuns {
		t.Fatal("'2' should switch to the Runs tab")
	}
	app.Update(keyRune('t'))
	if app.screen != screenThemeSelector {
		t.Fatal("'t' should open the theme selector")
	}
}

func TestModalInputStates(t *testing.T) {
	if !(EnginesTabModel{addNameMode: true}).InModalInput() {
		t.Fatal("engine name input must count as modal input")
	}
	if !(EnginesTabModel{renameMode: true}).InModalInput() {
		t.Fatal("engine rename input must count as modal input")
	}
	if !(ModelsTabModel{addingBrowse: true}).InModalInput() {
		t.Fatal("models file browser must count as modal input")
	}
	if !(ModelsTabModel{removeConfirm: true}).InModalInput() {
		t.Fatal("models remove confirm must count as modal input")
	}
	if (EnginesTabModel{}).InModalInput() || (ModelsTabModel{}).InModalInput() {
		t.Fatal("idle tabs must not report modal input")
	}
}

func TestTabBarShowsNumbersAndRunCount(t *testing.T) {
	home := HomeModel{
		activeTab: tabRuns,
		runsTab: RunsTabModel{runs: []RunSnapshot{
			{ID: 1}, {ID: 2, Stopped: true},
		}},
		width:  80,
		height: 20,
	}
	view := home.View()
	for _, want := range []string{"1 Profiles", "2 Runs (1)", "3 Models", "4 Engines"} {
		if !strings.Contains(view, want) {
			t.Fatalf("tab bar missing %q", want)
		}
	}
}
