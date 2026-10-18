package engine

import (
	"errors"
	"fmt"
	"maps"

	"github.com/dipankardas011/infai/pkg/agent/auditor"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/glue"
	"github.com/dipankardas011/infai/pkg/agent/store"
)

var (
	ErrPolicyNotFound = errors.New("policy not found")
	ErrPolicyExists   = errors.New("policy already exists")
	ErrPolicyInvalid  = errors.New("invalid policy")
)

func (e *InfaiAgentEngine) currentPolicies() map[string]*auditor.HITLPolicy {
	e.policyMu.RLock()
	defer e.policyMu.RUnlock()
	return e.hitlPolicies
}

func (e *InfaiAgentEngine) resolvePolicyName(name string) (*auditor.HITLPolicy, error) {
	if name == "" {
		name = auditor.DefaultHITLPolicyName
	}
	policy, ok := e.currentPolicies()[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrPolicyNotFound, name)
	}
	return policy, nil
}

func (e *InfaiAgentEngine) PolicyNames() []string {
	policies := e.currentPolicies()
	names := make([]string, 0, len(policies))
	for name := range policies {
		names = append(names, name)
	}

	return names
}

func (e *InfaiAgentEngine) ListPolicies() glue.HITLPoliciesOutput {
	policies := e.currentPolicies()
	names := e.PolicyNames()

	views := make([]glue.HITLPolicyView, 0, len(names))
	for _, name := range names {
		mappings := policies[name].ToolMappings
		decisions := make(map[string]string, len(mappings))

		for tool, decision := range mappings {
			decisions[string(tool)] = decision.String()
		}
		views = append(views, glue.HITLPolicyView{Name: name, Decisions: decisions})
	}
	return glue.HITLPoliciesOutput{Policies: views}
}

func (e *InfaiAgentEngine) CreatePolicy(name string, decisions contracts.HITLPolicyConfig) error {
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrPolicyInvalid)
	}
	if name == auditor.DefaultHITLPolicyName {
		return fmt.Errorf("%w: %q is built in", ErrPolicyInvalid, name)
	}
	if err := store.ValidatePolicy(name, decisions); err != nil {
		return fmt.Errorf("%w: %v", ErrPolicyInvalid, err)
	}
	policy, err := auditor.TranslateToHITLPolicy(name, decisions)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPolicyInvalid, err)
	}

	e.toolsConfigMu.Lock()
	defer e.toolsConfigMu.Unlock()

	e.policyMu.Lock()
	if _, exists := e.hitlPolicies[name]; exists {
		e.policyMu.Unlock()
		return fmt.Errorf("%w: %q", ErrPolicyExists, name)
	}
	policies := make(map[string]*auditor.HITLPolicy, len(e.hitlPolicies)+1)
	maps.Copy(policies, e.hitlPolicies)
	policies[name] = policy
	e.hitlPolicies = policies
	e.policyMu.Unlock()

	return e.persistPolicies(func(toolsConfig *store.ToolsConfig) {
		toolsConfig.HITLPolicies[name] = policy
	})
}

// DeletePolicy removes a policy from memory, then tells the store to persist
// the removal. Running sessions keep the instance they hold.
func (e *InfaiAgentEngine) DeletePolicy(name string) error {
	if name == auditor.DefaultHITLPolicyName {
		return fmt.Errorf("%w: %q is built in", ErrPolicyInvalid, name)
	}

	e.toolsConfigMu.Lock()
	defer e.toolsConfigMu.Unlock()

	e.policyMu.Lock()
	if _, ok := e.hitlPolicies[name]; !ok {
		e.policyMu.Unlock()
		return fmt.Errorf("%w: %q", ErrPolicyNotFound, name)
	}
	policies := make(map[string]*auditor.HITLPolicy, len(e.hitlPolicies)-1)
	for existingName, existingPolicy := range e.hitlPolicies {
		if existingName == name {
			continue
		}
		policies[existingName] = existingPolicy
	}
	e.hitlPolicies = policies
	e.policyMu.Unlock()

	return e.persistPolicies(func(toolsConfig *store.ToolsConfig) {
		delete(toolsConfig.HITLPolicies, name)
	})
}

// persistPolicies writes the HITL policies back to tools.json. The engine's
// memory is the running copy: the store is told after the fact, so a failed
// write leaves the running policy in place and reports the error.
func (e *InfaiAgentEngine) persistPolicies(mutate func(*store.ToolsConfig)) error {
	toolsConfig, err := store.LoadToolConfig()
	if err != nil {
		return err
	}
	mutate(&toolsConfig)
	return store.SaveToolConfig(toolsConfig)
}
