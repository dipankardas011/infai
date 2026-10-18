package session

import (
	"errors"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/auditor"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func (s *InfaiAgentSession) SetHITLPolicy(policy *auditor.HITLPolicy) error {
	if policy == nil {
		return errors.New("session: a HITL policy is required")
	}

	s.mu.Lock()
	s.hitlPolicy = policy
	name := policy.Name
	s.notifySubscribers(contracts.EventStream{
		Kind:      contracts.EventHITLPolicyChanged,
		Timestamp: time.Now().UTC(),
		Content:   &name,
	})
	s.mu.Unlock()
	return nil
}

func (s *InfaiAgentSession) ActivePolicyName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hitlPolicy.Name
}

func (s *InfaiAgentSession) policyDecision(tool contracts.ToolType) auditor.PolicyDecision {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hitlPolicy.Check(tool)
}
