package session

import (
	"github.com/dipankardas011/infai/pkg/agent/actuators"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/memory"
)

func (s *InfaiAgentSession) configureFileTools() {
	if len(s.availableTools) == 0 {
		s.availableTools = []contracts.Tool{}
	}
	s.availableTools = append(s.availableTools,
		actuators.ReadTool(),
		actuators.ListTool(),
		actuators.GlobTool(),
		actuators.SearchTool(),
		actuators.WriteTool(),
		actuators.EditTool(),
		actuators.BashTool(),
	)
}

func (s *InfaiAgentSession) configureMemoryTools() {
	if len(s.availableTools) == 0 {
		s.availableTools = []contracts.Tool{}
	}

	memoryTools := []contracts.Tool{}
	memoryTools = append(memoryTools, memory.ReadSkillTool(), memory.TaskChecklistTool())
	if s.skillRegistry != nil {
		s.availableSkills = s.skillRegistry.Skills()
	}

	s.availableTools = append(s.availableTools, memoryTools...)
}
