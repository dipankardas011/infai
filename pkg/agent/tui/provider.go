package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/glue"
	"github.com/dipankardas011/infai/pkg/agent/store"
)

type ProviderManagementClient interface {
	ListAllProviderModels(context.Context) ([]glue.ListModelOutput, error)
	LoginProvider(context.Context, contracts.ProviderSlug) error
	LogoutProvider(context.Context, contracts.ProviderSlug) error
}

func RunProviderLogin(ctx context.Context, client ProviderManagementClient, in io.Reader, out io.Writer) error {
	const custom = "Custom OpenAI-compatible"
	choice, err := runProviderMenu(ctx, in, out, "Provider login", "Select a managed provider", []string{
		"DeepSeek",
		"OpenAI Codex",
		custom,
	})
	if err != nil {
		return err
	}
	if choice == custom {
		return renderGenericProviderHelp(out)
	}
	providerID := contracts.DeepSeek
	if choice == "OpenAI Codex" {
		providerID = contracts.Codex
	}
	if err := client.LoginProvider(ctx, providerID); err != nil {
		return err
	}
	fmt.Fprintln(out, lipgloss.NewStyle().Foreground(everforest.Green).Render("Logged in to "+string(providerID)))
	return nil
}

func RunProviderLogout(ctx context.Context, client ProviderManagementClient, in io.Reader, out io.Writer) error {
	models, err := client.ListAllProviderModels(ctx)
	if err != nil {
		return err
	}
	configured := make(map[string]bool)
	for _, model := range models {
		if model.ProviderName == string(contracts.Codex) || model.ProviderName == string(contracts.DeepSeek) {
			configured[model.ProviderName] = true
		}
	}
	options := make([]string, 0, len(configured))
	for provider := range configured {
		options = append(options, provider)
	}
	sort.Strings(options)
	if len(options) == 0 {
		return errors.New("no managed providers are logged in")
	}
	choice, err := runProviderMenu(ctx, in, out, "Provider logout", "Select a configured provider", options)
	if err != nil {
		return err
	}
	if err := client.LogoutProvider(ctx, contracts.ProviderSlug(choice)); err != nil {
		return err
	}
	fmt.Fprintln(out, lipgloss.NewStyle().Foreground(everforest.Green).Render("Logged out of "+choice))
	return nil
}

func RunProviderList(ctx context.Context, client ProviderManagementClient, out io.Writer) error {
	models, err := client.ListAllProviderModels(ctx)
	if err != nil {
		return err
	}
	styles := newHarnessStyles()
	fmt.Fprintln(out, styles.screenTitle.Render("Provider models"))
	if len(models) == 0 {
		fmt.Fprintln(out, styles.screenBody.Render("No provider models configured."))
		return nil
	}
	for _, model := range models {
		thinking := make([]string, len(model.ThinkingLevels))
		for i, level := range model.ThinkingLevels {
			thinking[i] = string(level)
		}
		row := fmt.Sprintf("%-24s  %-16s  ctx %-9d  %s", model.ModelName, model.ProviderName, model.ContextWindow, strings.Join(thinking, ", "))
		fmt.Fprintln(out, styles.screenRow.Render(row))
	}
	return nil
}

func renderGenericProviderHelp(out io.Writer) error {
	root, err := store.Root()
	if err != nil {
		return err
	}
	styles := newHarnessStyles()
	schema := `{
  "providers": {
    "local": {
      "id": "openai-generic",
      "auth": { "method": "none" },
      "api_type": "openai-completions",
      "base_endpoint": "http://127.0.0.1:8000/v1",
      "models": {
        "local-model": {
          "id": "local-model",
          "name": "Local Model",
          "max_context_window": 64000,
          "max_output_tokens": 8192,
          "default_temperature": 0.2,
          "modality": ["text"],
          "available_thinking": false,
          "thinking_levels": {}
        }
      }
    }
  }
}`
	fmt.Fprintln(out, styles.screenTitle.Render("Custom OpenAI-compatible provider"))
	fmt.Fprintln(out, styles.screenBody.Render("Configure custom providers in:"))
	fmt.Fprintln(out, styles.active.Render(filepath.Join(root, "models.json")))
	fmt.Fprintln(out)
	fmt.Fprintln(out, styles.screenRow.Render(schema))
	return nil
}

type providerMenuModel struct {
	title, body string
	options     []string
	cursor      int
	selected    string
	canceled    bool
	width       int
	height      int
}

func (m *providerMenuModel) Init() tea.Cmd { return nil }

func (m *providerMenuModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyPressMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.options)-1 {
				m.cursor++
			}
		case "enter":
			m.selected = m.options[m.cursor]
			return m, tea.Quit
		case "esc", "ctrl+c":
			m.canceled = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *providerMenuModel) View() tea.View {
	styles := newHarnessStyles()
	lines := []string{styles.screenTitle.Render(m.title), styles.screenBody.Render(m.body), ""}
	for i, option := range m.options {
		marker := "○ "
		style := styles.menuRow
		if i == m.cursor {
			marker = "● "
			style = styles.menuActive
		}
		lines = append(lines, style.Render(marker+option))
	}
	lines = append(lines, "", styles.muted.Render("↑/↓ select  •  Enter confirm  •  Esc cancel"))
	content := styles.menu.Padding(1, 2).Render(strings.Join(lines, "\n"))
	if m.width > 0 && m.height > 0 {
		content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "infai provider"
	view.BackgroundColor = themeBackground()
	view.ForegroundColor = themeForeground()
	return view
}

func runProviderMenu(ctx context.Context, in io.Reader, out io.Writer, title, body string, options []string) (string, error) {
	model := &providerMenuModel{title: title, body: body, options: options}
	result, err := tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out)).Run()
	if err != nil {
		return "", err
	}
	selected := result.(*providerMenuModel)
	if selected.canceled || selected.selected == "" {
		return "", errors.New("provider selection canceled")
	}
	return selected.selected, nil
}
