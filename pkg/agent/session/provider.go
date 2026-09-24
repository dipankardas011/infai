package session

import (
	"errors"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/models"
)

func (s *InfaiAgentSession) CurrentSessionModelContextWindow() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.model.GetModelSpecs().Model().MaxContextLength
}

func (s *InfaiAgentSession) AvailableThinkingPatterns() []contracts.InfaiThinkingLevel {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.model.GetModelSpecs().Model().AvailableThinkingPatterns()
}

// SupportedModalities returns the input modalities declared by the session's
// currently selected model.
func (s *InfaiAgentSession) SupportedModalities() []contracts.LLMSupportedModality {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]contracts.LLMSupportedModality(nil), s.model.GetModelSpecs().Model().Modality...)
}

func (s *InfaiAgentSession) CurrentThinkingPattern() contracts.InfaiThinkingLevel {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.model.GetModelSpecs().ThinkingPattern()
}

// SetModel rebuilds the session's model adapter for the given provider and
// model and records the change in the session meta.
func (s *InfaiAgentSession) SetModel(choosenModel contracts.ProvisionedModel) error {
	model, err := models.ProvisionModelClient(choosenModel)
	if err != nil {
		return err
	}

	s.mu.Lock()
	if s.status != contracts.SessionIdle {
		s.mu.Unlock()
		return errors.New("session must be idle to change models")
	}
	s.model = model
	s.agent.SetModel(model)
	s.meta.Provider = model.GetModelSpecs().ProviderName()
	s.meta.Model = model.GetModelSpecs().Model().Id
	s.meta.UpdatedAt = time.Now().UTC()
	meta := s.meta
	s.mu.Unlock()
	if err := s.store.SaveMeta(meta); err != nil {
		s.l.Error("persist session metadata", "session_id", meta.ID, "error", err)
	}
	return nil
}

func (s *InfaiAgentSession) SetThinkingPattern(pattern contracts.InfaiThinkingLevel) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.status != contracts.SessionIdle {
		return errors.New("session must be idle to change thinking")
	}

	provisioned, err := s.model.GetModelSpecs().WithThinkingPattern(pattern)
	if err != nil {
		return err
	}
	model, err := models.ProvisionModelClient(provisioned)
	if err != nil {
		return err
	}

	if s.status != contracts.SessionIdle {
		return errors.New("session became busy while changing thinking")
	}
	s.model = model
	s.agent.SetModel(model)
	return nil
}

func (s *InfaiAgentSession) SetProviderAuth(providerName string, auth contracts.LLMProviderAuth) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.status != contracts.SessionIdle {
		return errors.New("session must be idle to refresh provider authentication")
	}
	current := s.model.GetModelSpecs()
	if current.ProviderName() != providerName {
		return nil
	}

	provisioned := contracts.NewProvisionedModel(
		current.ProviderSlug(), current.ProviderName(), current.BaseEndpoint(), current.APIType(), auth, current.Model(),
	)
	var err error
	if current.ThinkingPattern() != "" {
		provisioned, err = provisioned.WithThinkingPattern(current.ThinkingPattern())
		if err != nil {
			return err
		}
	}
	model, err := models.ProvisionModelClient(provisioned)
	if err != nil {
		return err
	}

	if s.status != contracts.SessionIdle {
		return errors.New("session became busy while refreshing provider authentication")
	}
	s.model = model
	s.agent.SetModel(model)

	return nil
}

func (s *InfaiAgentSession) ProviderAuthNeedsRefresh(now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	specs := s.model.GetModelSpecs()
	switch specs.ProviderSlug() {
	case contracts.Codex:
		auth := specs.Auth()
		if auth.Method != contracts.OAuth2 {
			return false, errors.New("openai codex auth: OAuth credentials are missing; run provider login again")
		}
		if auth.ExpiresAt == nil {
			return false, errors.New("openai codex auth: token expiry is missing; run provider login again")
		}
		if auth.ExpiresAt.After(now.Add(5 * time.Minute)) {
			if auth.AccessToken == "" || auth.AccountID == "" {
				return false, errors.New("openai codex auth: credentials are incomplete; run provider login again")
			}
			return false, nil
		}
		if auth.RefreshToken == "" {
			return false, errors.New("openai codex auth: refresh token is missing; run provider login again")
		}
		return true, nil

	default:
		return false, nil
	}
}
