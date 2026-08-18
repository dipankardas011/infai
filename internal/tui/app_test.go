package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dipankardas011/infai/internal/db"
	"github.com/dipankardas011/infai/internal/model"
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

func TestProfileEditViewFitsShortTerminalWithRecommendation(t *testing.T) {
	engines := []model.InferenceEngine{{ID: "e1", Name: "llama", Kind: model.EngineLlamaCPP}}
	em := NewProfileEditModel(model.ModelEntry{DisplayName: "m"}, engines, nil, 80, 20)
	em.SetRecommendation([]string{
		"131072 tokens | fits fit | high confidence",
		"required 4.8 GiB | available 7.5 GiB",
		"weights 2.9 GiB | KV 780 MiB | runtime 512 MiB | headroom 638 MiB",
		"reasons (5): context and hardware",
		"warnings (1): batch workspace is backend dependent",
	}, false)
	view := em.View()
	if lines := strings.Count(view, "\n") + 1; lines > 20 {
		t.Fatalf("profile editor exceeds terminal height: %d lines", lines)
	}
}

func TestProfileEditSpeculativeFieldsAreAdvancedAndConditional(t *testing.T) {
	target := model.ModelEntry{ID: 1, DisplayName: "target", Type: model.TypeGGUF, Metadata: `{"mtp_num_layers":2}`}
	draft := model.ModelEntry{ID: 2, DisplayName: "draft", Type: model.TypeGGUF}
	engines := []model.InferenceEngine{{ID: "e1", Name: "llama", Kind: model.EngineLlamaCPP}}
	em := NewProfileEditModel(target, engines, nil, 80, 24, []model.ModelEntry{target, draft})

	field := func(label string) *formField {
		for i := range em.fields {
			if em.fields[i].label == label {
				return &em.fields[i]
			}
		}
		t.Fatalf("field %q not found", label)
		return nil
	}
	field("Name").input.SetValue("native-mtp")
	mode := field("Speculative Decoding")
	if em.fieldVisible(*mode) {
		t.Fatal("speculative mode must be hidden until advanced configuration is enabled")
	}
	em, _ = em.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	if !em.fieldVisible(*field("Speculative Decoding")) {
		t.Fatal("speculative mode must be visible in advanced configuration")
	}
	for i, value := range mode.optionValues {
		if value == string(model.SpeculativeNativeMTP) {
			mode.selIdx = i
		}
	}
	if !em.fieldVisible(*field("Speculative Tokens")) {
		t.Fatal("token count must be visible when speculation is enabled")
	}
	if em.fieldVisible(*field("Draft / Assistant")) {
		t.Fatal("draft picker must remain hidden for native MTP")
	}

	p, err := em.ToProfile()
	if err != nil {
		t.Fatal(err)
	}
	if p.SpeculativeMode != model.SpeculativeNativeMTP || p.SpeculativeTokens == nil || *p.SpeculativeTokens != 1 {
		t.Fatalf("unexpected native MTP profile: %+v", p)
	}
}

func TestProfileEditSeparateSpeculativeModelRoundTrip(t *testing.T) {
	target := model.ModelEntry{ID: 1, DisplayName: "target", Type: model.TypeSafetensors}
	assistant := model.ModelEntry{ID: 2, DisplayName: "assistant", Type: model.TypeSafetensors}
	engines := []model.InferenceEngine{{ID: "e1", Name: "vLLM", Kind: model.EngineVLLM}}
	tokens := 2
	assistantID := assistant.ID
	profile := model.Profile{
		ID: 9, ModelID: target.ID, InferenceEngineID: "e1", Name: "mtp", Port: 8000,
		Host: "127.0.0.1", ContextSize: 4096, SpeculativeMode: model.SpeculativeMTPAssistant,
		SpeculativeTokens: &tokens, DraftModelID: &assistantID,
	}
	em := NewProfileEditModel(target, engines, &profile, 80, 24, []model.ModelEntry{target, assistant})
	if got := em.selectedFieldValue("Speculative Decoding"); got != string(model.SpeculativeMTPAssistant) {
		t.Fatalf("mode = %q, want MTP assistant", got)
	}
	if got := em.selectedFieldValue("Draft / Assistant"); got != "2" {
		t.Fatalf("assistant = %q, want 2", got)
	}

	got, err := em.ToProfile()
	if err != nil {
		t.Fatal(err)
	}
	if got.DraftModelID == nil || *got.DraftModelID != assistant.ID || got.SpeculativeTokens == nil || *got.SpeculativeTokens != tokens {
		t.Fatalf("speculative fields did not round trip: %+v", got)
	}
}

func TestProfilesTabShowsSpeculativeConfiguration(t *testing.T) {
	tokens := 2
	assistantID := int64(2)
	entry := db.ProfileEntry{
		Model:           model.ModelEntry{ID: 1, DisplayName: "target", Metadata: `{"mtp_num_layers":3}`},
		InferenceEngine: model.InferenceEngine{ID: "e1", Name: "llama.cpp", Kind: model.EngineLlamaCPP},
		Profile: model.Profile{
			ID: 1, ModelID: 1, InferenceEngineID: "e1", Name: "mtp", Host: "127.0.0.1", Port: 8000,
			SpeculativeMode: model.SpeculativeMTPAssistant, SpeculativeTokens: &tokens, DraftModelID: &assistantID,
		},
		DraftModelName: "mtp-target-q8",
	}
	m := NewProfilesTabModel(nil, []db.ProfileEntry{entry}, 120, 50)
	m.list.Select(1)
	m.updateViewport()
	view := m.viewport.View()
	for _, want := range []string{"Speculative Decoding", "MTP Assistant", "Draft Tokens", "mtp-target-q8", "3 layer(s) detected"} {
		if !strings.Contains(view, want) {
			t.Fatalf("profile preview missing %q:\n%s", want, view)
		}
	}

	entry.DraftModelName = ""
	m = NewProfilesTabModel(nil, []db.ProfileEntry{entry}, 120, 50)
	m.list.Select(1)
	m.updateViewport()
	if view := m.viewport.View(); !strings.Contains(view, "missing - select a model in edit") {
		t.Fatalf("profile preview does not identify deleted assistant:\n%s", view)
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

func TestIsNewerVersion(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"1.0.0", "v1.1.0", true},
		{"v1.0.0", "v1.1.0", true},
		{"1.1.0", "v1.1.0", false},
		{"1.2.0", "v1.1.9", false},
		{"1.0.0", "v2.0.0", true},
		{"1.0.0", "v1.0.1-rc1", true},
		{"dev", "v9.9.9", false},   // dev builds never nag
		{"", "v1.0.0", false},      // unknown current
		{"1.0.0", "", false},       // bad latest
		{"1.0.0", "banana", false}, // bad latest
	}
	for _, c := range cases {
		if got := isNewerVersion(c.current, c.latest); got != c.want {
			t.Errorf("isNewerVersion(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestUpdateNoticeFlow(t *testing.T) {
	app := newTestApp(t)

	// Failed/current check: nothing happens.
	app.Update(updateCheckMsg{})
	if app.screen != screenHome {
		t.Fatal("empty update result must not open the notice")
	}

	// Newer release: popup opens on home, enter dismisses.
	app.Update(updateCheckMsg{latest: "v99.0.0"})
	if app.screen != screenUpdateNotice {
		t.Fatal("update result should open the notice popup")
	}
	view := app.View()
	for _, want := range []string{"Update available", "v99.0.0"} {
		if !strings.Contains(view, want) {
			t.Fatalf("notice view missing %q", want)
		}
	}
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if app.screen != screenHome {
		t.Fatal("enter should dismiss the update notice")
	}

	// On any other screen the result becomes a toast, not a popup.
	app.screen = screenThemeSelector
	app.Update(updateCheckMsg{latest: "v99.0.1"})
	if app.screen != screenHome && app.screen != screenThemeSelector {
		t.Fatal("update result must not hijack a non-home screen")
	}
	if app.screen == screenUpdateNotice {
		t.Fatal("popup should not open over another screen")
	}
	if !strings.Contains(app.errMsg, "v99.0.1") {
		t.Fatalf("expected update toast, got %q", app.errMsg)
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
