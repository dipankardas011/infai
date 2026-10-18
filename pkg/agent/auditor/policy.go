package auditor

import (
	"fmt"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

type HITLPolicy struct {
	Name         string
	ToolMappings map[contracts.ToolType]PolicyDecision
}

type PolicyDecision string

const (
	DenyPolicy  PolicyDecision = "deny"
	HumanPolicy PolicyDecision = "human"
	AllowPolicy PolicyDecision = "allow"
)
const DefaultHITLPolicyName = "default"

func (p PolicyDecision) String() string { return string(p) }

func TranslateToHITLPolicy(name string, config contracts.HITLPolicyConfig) (*HITLPolicy, error) {
	mappings := make(map[contracts.ToolType]PolicyDecision)
	for tool, decisionStr := range config {
		switch PolicyDecision(decisionStr) {
		case DenyPolicy, AllowPolicy, HumanPolicy:
			mappings[contracts.ToolType(tool)] = PolicyDecision(decisionStr)
		default:
			return nil, fmt.Errorf("policy %q, tool %q: got: %s", name, tool, decisionStr)
		}
	}
	return &HITLPolicy{
		Name:         name,
		ToolMappings: mappings,
	}, nil
}

// NewDefaultHITLPolicy is the built-in policy. It lives in code and cannot
// be replaced from tools.json.
func NewDefaultHITLPolicy() *HITLPolicy {
	return &HITLPolicy{
		Name: DefaultHITLPolicyName,
		ToolMappings: map[contracts.ToolType]PolicyDecision{
			contracts.ReadTool:          AllowPolicy,
			contracts.ListTool:          AllowPolicy,
			contracts.GlobTool:          AllowPolicy,
			contracts.SearchTool:        AllowPolicy,
			contracts.WriteTool:         HumanPolicy,
			contracts.EditTool:          HumanPolicy,
			contracts.BashTool:          HumanPolicy,
			contracts.ReadSkillTool:     AllowPolicy,
			contracts.TaskChecklistTool: AllowPolicy,
			contracts.WebFetchTool:      HumanPolicy,
			contracts.WebSearchTool:     HumanPolicy,

			contracts.DelegateBackgroundSidecarLoopTool: HumanPolicy,
			contracts.DelegateSidecarLoopTool:           HumanPolicy,
		},
	}
}

func (perm *HITLPolicy) Check(tool contracts.ToolType) PolicyDecision {
	if policy, ok := perm.ToolMappings[tool]; ok {
		return policy
	}
	return HumanPolicy
}
