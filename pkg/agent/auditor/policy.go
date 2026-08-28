package auditor

import "github.com/dipankardas011/infai/pkg/agent/contracts"

type AuditorPolicy struct {
	ToolMappings map[contracts.ToolType]PolicyDecision
}

type PolicyDecision int

const (
	DenyPolicy PolicyDecision = iota
	HumanPolicy
	AllowPolicy
)

func NewAuditorPolicy() *AuditorPolicy {
	return &AuditorPolicy{
		ToolMappings: map[contracts.ToolType]PolicyDecision{
			contracts.ReadTool: DenyPolicy,
		},
	}
}

func (perm *AuditorPolicy) SetPolicy(tool contracts.ToolType, policy PolicyDecision) {
	perm.ToolMappings[tool] = policy
}

func (perm *AuditorPolicy) Check(tool contracts.ToolType) PolicyDecision {
	if policy, ok := perm.ToolMappings[tool]; ok {
		return policy
	}
	return DenyPolicy
}
