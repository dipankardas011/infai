package session

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
)

func (s *InfaiAgentSession) publishTaskChecklist() error {
	content, err := json.Marshal(s.taskChecklist.Snapshot())
	if err != nil {
		return fmt.Errorf("encode task checklist delta: %w", err)
	}
	s.events.Publish(store.Record{
		Kind:      store.KindDelta,
		Timestamp: time.Now().UTC(),
		DeltaKind: contracts.DeltaTaskChecklist,
		Text:      string(content),
	})
	return nil
}
