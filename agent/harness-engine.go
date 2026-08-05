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
