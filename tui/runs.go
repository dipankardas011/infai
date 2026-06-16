package tui

import (
	"fmt"
	"time"
)

type RunID int64

type RunSnapshot struct {
	ID            RunID
	ProfileID     int64
	ModelID       int64
	ProfileName   string
	ModelName     string
	ModelType     string
	Host          string
	RequestedPort int
	ActualPort    int
	PID           int
	StartedAt     time.Time
	StoppedAt     time.Time
	Stopped       bool
	Stopping      bool
	ForceKilled   bool
	ExitErr       error
	LogCount      int
	SystemUsage   string
	LiveTPS       float64
	Active        int
	Deferred      int
}

type RunRecord struct {
	ID            RunID
	ProfileID     int64
	ModelID       int64
	ProfileName   string
	ModelName     string
	ModelType     string
	Host          string
	RequestedPort int
	ActualPort    int
	Server        ServerModel
}

type RunsStore struct {
	nextID RunID
	order  []RunID
	runs   map[RunID]*RunRecord
	active RunID
}

func NewRunsStore() RunsStore {
	return RunsStore{nextID: 1, runs: make(map[RunID]*RunRecord)}
}

func (s *RunsStore) NewID() RunID {
	if s.nextID <= 0 {
		s.nextID = 1
	}
	id := s.nextID
	s.nextID++
	return id
}

func (s *RunsStore) Add(r RunRecord) RunID {
	if s.runs == nil {
		s.runs = make(map[RunID]*RunRecord)
	}
	if r.ID == 0 {
		r.ID = s.NewID()
	}
	id := r.ID
	s.runs[id] = &r
	s.order = append(s.order, id)
	s.active = id
	return id
}

func (s *RunsStore) Get(id RunID) (*RunRecord, bool) {
	if s.runs == nil || id == 0 {
		return nil, false
	}
	r, ok := s.runs[id]
	return r, ok
}

func (s *RunsStore) Active() (*RunRecord, bool) { return s.Get(s.active) }

func (s *RunsStore) SetActive(id RunID) bool {
	if _, ok := s.Get(id); !ok {
		return false
	}
	s.active = id
	return true
}

func (s *RunsStore) RemoveStopped(id RunID) bool {
	r, ok := s.Get(id)
	if !ok || !r.Server.stopped || r.Server.stopping {
		return false
	}
	delete(s.runs, id)
	filtered := s.order[:0]
	for _, existing := range s.order {
		if existing != id {
			filtered = append(filtered, existing)
		}
	}
	s.order = filtered
	if s.active == id {
		s.active = 0
		if len(s.order) > 0 {
			s.active = s.order[len(s.order)-1]
		}
	}
	return true
}

func (s *RunsStore) Snapshot() []RunSnapshot {
	out := make([]RunSnapshot, 0, len(s.order))
	for i := len(s.order) - 1; i >= 0; i-- {
		id := s.order[i]
		r, ok := s.Get(id)
		if !ok {
			continue
		}
		out = append(out, r.Snapshot())
	}
	return out
}

func (s *RunsStore) RunningProfileCounts() map[int64]int {
	counts := make(map[int64]int)
	for _, id := range s.order {
		r, ok := s.Get(id)
		if !ok || r.Server.stopped {
			continue
		}
		counts[r.ProfileID]++
	}
	return counts
}

func (s *RunsStore) HasActiveProfile(profileID int64) bool {
	_, ok := s.ActiveProfileRun(profileID)
	return ok
}

func (s *RunsStore) ActiveProfileRun(profileID int64) (RunID, bool) {
	for i := len(s.order) - 1; i >= 0; i-- {
		id := s.order[i]
		r, ok := s.Get(id)
		if ok && r.ProfileID == profileID && !r.Server.stopped {
			return id, true
		}
	}
	return 0, false
}

func (s *RunsStore) OccupiedPorts() map[int]bool {
	ports := make(map[int]bool)
	for _, id := range s.order {
		r, ok := s.Get(id)
		if ok && !r.Server.stopped && r.ActualPort > 0 {
			ports[r.ActualPort] = true
		}
	}
	return ports
}

func (s *RunsStore) SetSize(w, h int) {
	for _, r := range s.runs {
		r.Server = r.Server.SetSize(w, h)
	}
}

func (s *RunsStore) ActiveCount() int {
	count := 0
	for _, id := range s.order {
		r, ok := s.Get(id)
		if ok && !r.Server.stopped {
			count++
		}
	}
	return count
}

func (s *RunsStore) NextActive(delta int) bool {
	if len(s.order) == 0 {
		return false
	}
	idx := -1
	for i, id := range s.order {
		if id == s.active {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.active = s.order[len(s.order)-1]
		return true
	}
	idx = (idx + delta + len(s.order)) % len(s.order)
	s.active = s.order[idx]
	return true
}

func (r *RunRecord) Snapshot() RunSnapshot {
	s := r.Server
	pid := 0
	if s.process != nil {
		pid = s.process.PID()
	}
	return RunSnapshot{
		ID: r.ID, ProfileID: r.ProfileID, ModelID: r.ModelID,
		ProfileName: r.ProfileName, ModelName: r.ModelName, ModelType: r.ModelType,
		Host: r.Host, RequestedPort: r.RequestedPort, ActualPort: r.ActualPort,
		PID: pid, StartedAt: s.startedAt, StoppedAt: s.stoppedAt,
		Stopped: s.stopped, Stopping: s.stopping, ForceKilled: s.forceKilled,
		ExitErr: s.exitErr, LogCount: len(s.logs), SystemUsage: s.systemUsage, LiveTPS: s.liveTPS,
		Active: s.liveActive, Deferred: s.liveDeferred,
	}
}

func runEndpoint(host string, port int) string {
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d/v1", host, port)
}
