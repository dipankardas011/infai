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
