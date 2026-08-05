package agent

import (
	"github.com/dipankardas011/infai/agent/contracts"
	_ "go.uber.org/automaxprocs"
)

type HarnessEngine struct {
	modelProvider contracts.InfaiModelAdaptor
}

func (*HarnessEngine) Start() {}
func (*HarnessEngine) Stop()  {}

// we need the infai to have a tool call with git which helps to get most out of it interms of lines its focusing to get the previous history given to it what and why it was changed like those.
// also for compaction latest information is put forward, then middle sweep and and a whole summary like thing just like deepseek and others.
// need to understand if the compaction is a process of harness in anyway helping the model or its just the model who decides the underlaying strategy.
// and if we can use a seperate model which is crazy good at compaction like deepseek.
