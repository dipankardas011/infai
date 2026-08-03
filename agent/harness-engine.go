package agent

import "github.com/dipankardas011/infai/agent/models"

type HarnessEngine struct {
	modelProvider models.InfaiModelAdaptor
}

func (*HarnessEngine) Start() {}
