package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/glue"
	"github.com/dipankardas011/infai/pkg/agent/store"
)

type ProviderManagementClient interface {
	ListAllProviderModels(context.Context) ([]glue.ListModelOutput, error)
	ProviderAuthMethods(context.Context, contracts.ProviderSlug) ([]contracts.ProviderAuthMethod, error)
	LoginProvider(context.Context, glue.LoginProviderInput) (*glue.LoginProviderOutput, error)
	ProviderLoginStatus(context.Context, contracts.ProviderSlug) (*glue.LoginProviderOutput, error)
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
	methods, err := client.ProviderAuthMethods(ctx, providerID)
	if err != nil {
		return err
	}
	if len(methods) == 0 {
		return fmt.Errorf("provider %q has no supported authentication methods", providerID)
	}
	method := methods[0]
	if len(methods) > 1 {
		options := make([]string, len(methods))
		for i := range methods {
			options[i] = methods[i].Name
		}
		selected, err := runProviderMenu(ctx, in, out, "Authentication", "Select an authentication method", options)
		if err != nil {
			return err
		}
		for _, candidate := range methods {
			if candidate.Name == selected {
				method = candidate
				break
			}
		}
	}

	var credential string
	if method.SecretInput {
		credential, err = readProviderSecret(in, out, method.Name+": ")
		if err != nil {
			return err
		}
	}
	result, err := client.LoginProvider(ctx, glue.LoginProviderInput{
		ProviderID: providerID, Method: method.Method, Credential: credential,
	})
	if err != nil {
		return err
	}
	if result.Challenge.VerificationURL != "" {
		styles := newHarnessStyles()
		fmt.Fprintln(out, styles.screenBody.Render("Open this URL to authorize infaiw:"))
		fmt.Fprintln(out, styles.active.Render(result.Challenge.VerificationURL))
		if result.Challenge.UserCode != "" {
			fmt.Fprintln(out, styles.screenBody.Render("Enter code: ")+styles.active.Render(result.Challenge.UserCode))
		}
	}
	for result.Status == contracts.ProviderAuthPending {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		result, err = client.ProviderLoginStatus(ctx, providerID)
		if err != nil {
			return err
		}
	}
	if result.Status == contracts.ProviderAuthFailed {
		if result.Error == "" {
			result.Error = "authentication failed"
		}
		return errors.New(result.Error)
	}
	if result.Status != contracts.ProviderAuthComplete {
		return fmt.Errorf("provider authentication returned unknown status %q", result.Status)
	}
	fmt.Fprintln(out, lipgloss.NewStyle().Foreground(everforest.Green).Render("Logged in to "+string(providerID)))
	return nil
}

func readProviderSecret(in io.Reader, out io.Writer, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	if file, ok := in.(*os.File); ok && term.IsTerminal(file.Fd()) {
		value, err := term.ReadPassword(file.Fd())
		fmt.Fprintln(out)
		if err != nil {
			return "", err
		}
		if secret := strings.TrimSpace(string(value)); secret != "" {
			return secret, nil
		}
		return "", errors.New("credential is required")
	}
	value, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if secret := strings.TrimSpace(value); secret != "" {
		return secret, nil
	}
	return "", errors.New("credential is required")
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
