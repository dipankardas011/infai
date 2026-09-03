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

func (p PolicyDecision) String() string {
	switch p {
	case HumanPolicy:
		return "human"
	case AllowPolicy:
		return "allow"
	default:
		return "deny"
	}
}

func NewAuditorPolicy() *AuditorPolicy {
	return &AuditorPolicy{
		ToolMappings: map[contracts.ToolType]PolicyDecision{
			contracts.ReadTool:      AllowPolicy,
			contracts.ListTool:      AllowPolicy,
			contracts.GlobTool:      AllowPolicy,
			contracts.SearchTool:    AllowPolicy,
			contracts.WriteTool:     HumanPolicy,
			contracts.EditTool:      HumanPolicy,
			contracts.BashTool:      HumanPolicy,
			contracts.ReadSkillTool: AllowPolicy,
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
