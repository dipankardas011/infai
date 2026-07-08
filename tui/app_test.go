package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dipankardas011/infai/db"
	"github.com/dipankardas011/infai/model"
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

func TestProfileEditDirtyCheck(t *testing.T) {
	engines := []model.InferenceEngine{{ID: "e1", Name: "cpu"}}
	em := NewProfileEditModel(model.ModelEntry{DisplayName: "m"}, engines, nil, 80, 24)
	if em.dirty() {
		t.Fatal("freshly opened editor must not be dirty")
	}
	em, _ = em.Update(keyRune('x')) // types into the focused Name field
	if !em.dirty() {
		t.Fatal("editor must be dirty after typing")
	}
}

// Esc on a dirty editor must prompt instead of silently discarding; 'n' keeps
// editing, 'y' leaves the screen.
func TestProfileEditEscConfirmsDiscard(t *testing.T) {
	app := newTestApp(t)
	engines := []model.InferenceEngine{{ID: "e1", Name: "cpu"}}
	app.profileEdit = NewProfileEditModel(model.ModelEntry{DisplayName: "m"}, engines, nil, 80, 24)
	app.profileEditReturn = screenHome
	app.screen = screenProfileEdit

	esc := tea.KeyMsg{Type: tea.KeyEsc}

	// Clean editor: esc leaves immediately.
	app.Update(esc)
	if app.screen != screenHome {
		t.Fatal("esc on a clean editor should leave the screen")
	}

	// Dirty editor: esc prompts, n cancels, y discards.
	app.screen = screenProfileEdit
	app.Update(keyRune('x'))
	app.Update(esc)
	if app.screen != screenProfileEdit || !app.profileEdit.discardConfirm {
		t.Fatal("esc on a dirty editor should open the discard prompt")
	}
	app.Update(keyRune('n'))
	if app.screen != screenProfileEdit || app.profileEdit.discardConfirm {
		t.Fatal("'n' should keep editing")
	}
	app.Update(esc)
	app.Update(keyRune('y'))
	if app.screen != screenHome {
		t.Fatal("'y' should discard and leave the editor")
	}
}

func TestSparkline(t *testing.T) {
	if got := renderSparkline(nil, 10); got != "" {
		t.Fatalf("empty history should render nothing, got %q", got)
	}
	if got := renderSparkline([]float64{5}, 10); got != "" {
		t.Fatalf("single sample should render nothing, got %q", got)
	}
	if got := renderSparkline([]float64{1, 8}, 10); got != "▁█" {
		t.Fatalf("min/max should map to lowest/highest bars, got %q", got)
	}
	if got := renderSparkline([]float64{5, 5, 5}, 10); got != "▅▅▅" {
		t.Fatalf("constant history should render mid bars, got %q", got)
	}
	// More samples than width: only the most recent `width` samples survive.
	long := make([]float64, 50)
	for i := range long {
		long[i] = float64(i)
	}
	if got := []rune(renderSparkline(long, 8)); len(got) != 8 {
		t.Fatalf("sparkline should clamp to width 8, got %d runes", len(got))
	}
}

func TestThemesHaveCompletePalettes(t *testing.T) {
	seen := map[string]bool{}
	for _, th := range ThemeList {
		if th.Name == "" || seen[th.Name] {
			t.Fatalf("theme name empty or duplicated: %q", th.Name)
		}
		seen[th.Name] = true
		for name, c := range map[string]string{
			"Primary": string(th.Primary), "Secondary": string(th.Secondary),
			"Success": string(th.Success), "Warning": string(th.Warning),
			"Error": string(th.Error), "Muted": string(th.Muted),
			"Text": string(th.Text), "Bg": string(th.Bg), "Surface": string(th.Surface),
		} {
			if len(c) != 7 || c[0] != '#' {
				t.Fatalf("theme %s: %s color %q is not a #RRGGBB value", th.Name, name, c)
			}
		}
	}
}

func TestThemeSelectorLivePreviewAndRevert(t *testing.T) {
	SetTheme("tokyonight")
	sel := NewThemeSelectorModel(80, 24)
	sel, _ = sel.Update(tea.KeyMsg{Type: tea.KeyDown})
	if ActiveTheme.Name == "tokyonight" {
		t.Fatal("moving the cursor should live-preview the highlighted theme")
	}
	sel.Revert()
	if ActiveTheme.Name != "tokyonight" {
		t.Fatalf("revert should restore the original theme, got %s", ActiveTheme.Name)
	}
}

func TestLastTabPersistsAcrossSessions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	database, err := db.Open()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	app := NewApp(database, nil, nil, 80, 24)
	(&app).Update(keyRune('3'))
	if app.home.activeTab != tabModels {
		t.Fatal("'3' should switch to the Models tab")
	}
	database.Close()

	database2, err := db.Open()
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer database2.Close()
	app2 := NewApp(database2, nil, nil, 80, 24)
	if app2.home.activeTab != tabModels {
		t.Fatalf("new session should reopen on the Models tab, got tab %d", app2.home.activeTab)
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
