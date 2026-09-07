package providercli

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/models"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/spf13/cobra"
)

type streams struct {
	in     io.Reader
	out    io.Writer
	errOut io.Writer
}

// NewCommand creates the complete provider management command tree.
func NewCommand(in io.Reader, out, errOut io.Writer) *cobra.Command {
	s := streams{in: in, out: out, errOut: errOut}
	cmd := &cobra.Command{Use: "provider", Short: "manage model providers", Args: cobra.NoArgs}
	cmd.AddCommand(
		&cobra.Command{Use: "login", Short: "log in to a provider", Args: cobra.NoArgs, RunE: s.login},
		&cobra.Command{Use: "logout", Short: "log out of a provider", Args: cobra.NoArgs, RunE: s.logout},
		&cobra.Command{Use: "list", Short: "list configured provider models", Args: cobra.NoArgs, RunE: s.list},
	)
	model := &cobra.Command{Use: "model", Short: "manage provider models", Args: cobra.NoArgs}
	model.AddCommand(
		&cobra.Command{Use: "add", Short: "add a provider model", Args: cobra.NoArgs, RunE: s.addModel},
		&cobra.Command{Use: "remove", Short: "remove a provider model", Args: cobra.NoArgs, RunE: s.removeModel},
		&cobra.Command{Use: "edit", Short: "edit a provider model", Args: cobra.NoArgs, RunE: s.editModel},
	)
	cmd.AddCommand(model)
	cmd.SetIn(in)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	return cmd
}

func (s streams) interactive() error {
	in, inOK := s.in.(interface{ Fd() uintptr })
	out, outOK := s.out.(interface{ Fd() uintptr })
	if !inOK || !outOK || !term.IsTerminal(in.Fd()) || !term.IsTerminal(out.Fd()) {
		return errors.New("provider command requires an interactive terminal")
	}
	return nil
}

func openStore() (*store.ProviderStore, error) { return store.OpenProviderStore() }

func (s streams) login(cmd *cobra.Command, _ []string) error {
	if err := s.interactive(); err != nil {
		return err
	}
	registry, err := openStore()
	if err != nil {
		return err
	}
	providers := registry.List()
	choices := providerNames(providers)
	const create = "[new provider]"
	choices = append(choices, create)
	choice, err := runMenu(s.in, s.out, "Provider login", choices)
	if err != nil {
		return err
	}
	var config store.Provider
	created := false
	if choice == create {
		const codex = "OpenAI Codex"
		const custom = "Custom OpenAI compatible"
		catalog, catalogErr := models.ListProviderCatalog(cmd.Context())
		providerChoices := []string{codex}
		catalogByLabel := make(map[string]models.ProviderCatalogEntry, len(catalog))
		for _, entry := range catalog {
			label := entry.Name + " (" + entry.ID + ")"
			providerChoices = append(providerChoices, label)
			catalogByLabel[label] = entry
		}
		providerChoices = append(providerChoices, custom)
		providerChoice, err := runMenu(s.in, s.out, "Provider", providerChoices)
		if err != nil {
			return err
		}
		switch providerChoice {
		case codex:
			config.ID = contracts.ProviderIDOpenAICodex
		case custom:
			config.ID, err = requiredInput(s.in, s.out, "Provider ID", "", false)
			if err == nil {
				config.BaseEndpoint, err = requiredInput(s.in, s.out, "Base endpoint", "", false)
			}
		default:
			config.ID = catalogByLabel[providerChoice].ID
		}
		if err != nil {
			return err
		}
		if catalogErr != nil && providerChoice != codex && providerChoice != custom {
			return catalogErr
		}
		if err := registry.AddProvider(config); err != nil {
			return err
		}
		created = true
	} else {
		var ok bool
		config, ok = registry.Get(choice)
		if !ok {
			return fmt.Errorf("provider %q no longer exists", choice)
		}
	}
	provider, err := models.NewLLMProvider(config, registry)
	if err != nil {
		if created {
			_ = registry.RemoveProvider(config.ID)
		}
		return err
	}
	request, err := s.loginRequest(config.ID)
	if err == nil {
		err = provider.Login(cmd.Context(), request)
	}
	if err != nil {
		if created {
			if cleanupErr := registry.RemoveProvider(config.ID); cleanupErr != nil {
				return fmt.Errorf("login failed: %w; cleanup failed: %v", err, cleanupErr)
			}
		}
		return err
	}
	fmt.Fprintf(s.out, "Logged in to %s.\n", config.ID)
	return nil
}

func (s streams) loginRequest(providerID string) (contracts.ProviderLoginRequest, error) {
	if providerID == contracts.ProviderIDOpenAICodex {
		method, err := runMenu(s.in, s.out, "Login method", []string{"Browser", "Device code"})
		if err != nil {
			return contracts.ProviderLoginRequest{}, err
		}
		value := "browser"
		if method == "Device code" {
			value = "device_code"
		}
		return contracts.ProviderLoginRequest{Method: value, Notify: s.notifyLogin}, nil
	}
	method, err := runMenu(s.in, s.out, "Authentication", []string{"None", "Basic", "Bearer token"})
	if err != nil {
		return contracts.ProviderLoginRequest{}, err
	}
	request := contracts.ProviderLoginRequest{Method: contracts.AuthTypeNone}
	switch method {
	case "Basic":
		request.Method = contracts.AuthTypeBasic
		request.Username, err = runInput(s.in, s.out, "Username", "", false)
		if err == nil {
			request.Password, err = runInput(s.in, s.out, "Password", "", true)
		}
	case "Bearer token":
		request.Method = contracts.AuthTypeBearer
		request.Token, err = requiredInput(s.in, s.out, "Bearer token", "", true)
	}
	return request, err
}

func (s streams) notifyLogin(event contracts.ProviderLoginEvent) error {
	fmt.Fprintf(s.out, "\nOpen: %s\n", event.URL)
	if event.UserCode != "" {
		fmt.Fprintf(s.out, "Code: %s\n", event.UserCode)
	}
	launchBrowser(event.URL)
	return nil
}

func launchBrowser(url string) {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{url}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		name, args = "xdg-open", []string{url}
	}
	_ = exec.Command(name, args...).Start()
}

func (s streams) logout(_ *cobra.Command, _ []string) error {
	if err := s.interactive(); err != nil {
		return err
	}
	registry, err := openStore()
	if err != nil {
		return err
	}
	var names []string
	for _, provider := range registry.List() {
		if !provider.Auth.Empty() {
			names = append(names, provider.ID)
		}
	}
	if len(names) == 0 {
		return errors.New("no logged-in providers")
	}
	name, err := runMenu(s.in, s.out, "Provider logout", names)
	if err != nil {
		return err
	}
	confirm, err := runMenu(s.in, s.out, "Clear stored authentication?", []string{"No", "Yes"})
	if err != nil {
		return err
	}
	if confirm != "Yes" {
		return errCanceled
	}
	config, ok := registry.Get(name)
	if !ok {
		return fmt.Errorf("provider %q no longer exists", name)
	}
	provider, err := models.NewLLMProvider(config, registry)
	if err != nil {
		return err
	}
	if err := provider.Logout(); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "Logged out of %s.\n", name)
	return nil
}

func (s streams) list(_ *cobra.Command, _ []string) error {
	registry, err := openStore()
	if err != nil {
		return err
	}
	providers := registry.List()
	if len(providers) == 0 {
		fmt.Fprintln(s.out, "No providers configured. Run `infaiw provider login`.")
		return nil
	}
	printed := false
	for _, provider := range providers {
		if len(provider.Models) == 0 {
			endpoint := provider.BaseEndpoint
			if endpoint == "" {
				endpoint = "-"
			}
			fmt.Fprintf(s.out, "%s  base_endpoint=%s auth=%s models=none\n", provider.ID, endpoint, authStatus(provider.Auth))
			printed = true
		}
		for _, key := range provider.ModelNames() {
			model := provider.Models[key]
			endpoint := provider.BaseEndpoint
			if endpoint == "" {
				endpoint = "-"
			}
			modes := "-"
			if len(model.ThinkingModes) > 0 {
				modes = strings.Join(model.ThinkingModes, ",")
			}
			effort := model.ReasoningEffort
			if effort == "" {
				effort = "-"
			}
			fmt.Fprintf(s.out, "%s@%s  base_endpoint=%s auth=%s context=%d thinking=%s effort=%s\n", key, provider.ID, endpoint, authStatus(provider.Auth), model.ContextWindow, modes, effort)
			printed = true
		}
	}
	if !printed {
		fmt.Fprintln(s.out, "No provider models configured. Run `infaiw provider model add`.")
	}
	return nil
}

func authStatus(auth store.Auth) string {
	if auth.Empty() {
		return "not-configured"
	}
	switch auth.Type {
	case contracts.AuthTypeNone:
		return "none"
	case contracts.AuthTypeBasic, contracts.AuthTypeBearer, contracts.AuthTypeOAuth:
		if auth.Authenticated() {
			return auth.Type + "(configured)"
		}
		return auth.Type + "(incomplete)"
	default:
		return "unknown"
	}
}

func (s streams) addModel(_ *cobra.Command, _ []string) error {
	if err := s.interactive(); err != nil {
		return err
	}
	registry, err := openStore()
	if err != nil {
		return err
	}
	name, err := chooseProvider(s, registry.List(), false)
	if err != nil {
		return err
	}
	key, model, err := s.modelForm("", store.Model{})
	if err != nil {
		return err
	}
	if err := registry.AddModel(name, key, model); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "Added %s@%s.\n", key, name)
	return nil
}

func (s streams) removeModel(_ *cobra.Command, _ []string) error {
	if err := s.interactive(); err != nil {
		return err
	}
	registry, err := openStore()
	if err != nil {
		return err
	}
	providerName, modelName, _, err := chooseModel(s, registry)
	if err != nil {
		return err
	}
	confirm, err := runMenu(s.in, s.out, "Remove "+modelName+"@"+providerName+"?", []string{"No", "Yes"})
	if err != nil {
		return err
	}
	if confirm != "Yes" {
		return errCanceled
	}
	if err := registry.RemoveModel(providerName, modelName); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "Removed %s@%s.\n", modelName, providerName)
	return nil
}

func (s streams) editModel(_ *cobra.Command, _ []string) error {
	if err := s.interactive(); err != nil {
		return err
	}
	registry, err := openStore()
	if err != nil {
		return err
	}
	providerName, modelName, current, err := chooseModel(s, registry)
	if err != nil {
		return err
	}
	_, updated, err := s.modelForm(modelName, current)
	if err != nil {
		return err
	}
	if err := registry.UpdateModel(providerName, modelName, updated); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "Updated %s@%s.\n", modelName, providerName)
	return nil
}

func (s streams) modelForm(keyInitial string, initial store.Model) (string, store.Model, error) {
	key := keyInitial
	var err error
	if key == "" {
		key, err = requiredInput(s.in, s.out, "Model key", "", false)
		if err != nil {
			return "", store.Model{}, err
		}
	}
	apiID, err := runInput(s.in, s.out, "API model ID (optional)", initial.Name, false)
	if err != nil {
		return "", store.Model{}, err
	}
	contextValue := ""
	if initial.ContextWindow > 0 {
		contextValue = strconv.Itoa(initial.ContextWindow)
	}
	contextText, err := requiredInput(s.in, s.out, "Context length", contextValue, false)
	if err != nil {
		return "", store.Model{}, err
	}
	contextWindow, err := strconv.Atoi(contextText)
	if err != nil || contextWindow <= 0 {
		return "", store.Model{}, errors.New("context length must be a positive integer")
	}
	modes, err := runInput(s.in, s.out, "Thinking modes (comma separated)", strings.Join(initial.ThinkingModes, ","), false)
	if err != nil {
		return "", store.Model{}, err
	}
	effort, err := runInput(s.in, s.out, "Selected effort (optional)", initial.ReasoningEffort, false)
	if err != nil {
		return "", store.Model{}, err
	}
	model := store.Model{Name: apiID, ContextWindow: contextWindow, ThinkingModes: splitModes(modes), ReasoningEffort: effort}
	return key, model, nil
}

func splitModes(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func chooseProvider(s streams, providers []store.Provider, requireModels bool) (string, error) {
	var names []string
	for _, provider := range providers {
		if !requireModels || len(provider.Models) > 0 {
			names = append(names, provider.ID)
		}
	}
	if len(names) == 0 {
		if requireModels {
			return "", errors.New("no provider models configured")
		}
		return "", errors.New("no providers configured")
	}
	return runMenu(s.in, s.out, "Provider", names)
}

func chooseModel(s streams, registry *store.ProviderStore) (string, string, store.Model, error) {
	providerName, err := chooseProvider(s, registry.List(), true)
	if err != nil {
		return "", "", store.Model{}, err
	}
	provider, ok := registry.Get(providerName)
	if !ok {
		return "", "", store.Model{}, fmt.Errorf("provider %q no longer exists", providerName)
	}
	modelName, err := runMenu(s.in, s.out, "Model", provider.ModelNames())
	if err != nil {
		return "", "", store.Model{}, err
	}
	return providerName, modelName, provider.Models[modelName], nil
}

func providerNames(providers []store.Provider) []string {
	names := make([]string, 0, len(providers))
	for _, provider := range providers {
		names = append(names, provider.ID)
	}
	sort.Strings(names)
	return names
}
