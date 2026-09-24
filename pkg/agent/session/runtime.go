package session

import (
	"sync"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	harnessErr "github.com/dipankardas011/infai/pkg/agent/errors"
	"github.com/dipankardas011/infai/pkg/agent/glue"
	"github.com/dipankardas011/infai/pkg/agent/store"
)

const (
	MAX_SESSION_CLIENTS = 2

	SUBSCRIBER_BUFFER = 64
)

func (s *InfaiAgentSession) JoinSessionEvents() (glue.SessionView, <-chan contracts.EventStream, func(), error) {
	s.mu.Lock()

	select {
	case <-s.ctx.Done():
		s.mu.Unlock()
		return glue.SessionView{}, nil, nil, harnessErr.ErrSessionClosed
	default:
		if s.status == contracts.SessionTombstone {
			s.mu.Unlock()
			return glue.SessionView{}, nil, nil, harnessErr.ErrSessionClosed
		}
		if len(s.subscribers) >= MAX_SESSION_CLIENTS {
			s.mu.Unlock()
			return glue.SessionView{}, nil, nil, harnessErr.ErrTooManyClients
		}
	}

	sub := &subscriber{events: make(chan contracts.EventStream, SUBSCRIBER_BUFFER)}
	s.subscribers[sub] = struct{}{}
	view := glue.SessionView{
		Meta:     s.meta,
		History:  append([]contracts.ChatMessage(nil), s.activeTimeline...),
		Status:   s.status,
		InFlight: append([]contracts.EventStream(nil), s.inFlight...),
	}
	if s.pendingApproval != nil {
		request := s.pendingApproval.request
		view.PendingApproval = &request
	}
	s.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			s.mu.Lock()
			if _, ok := s.subscribers[sub]; ok {
				delete(s.subscribers, sub)
				close(sub.events)
			}
			s.mu.Unlock()
		})
	}
	return view, sub.events, unsubscribe, nil
}

// recordSessionConclusion writes how the session ended to its index, once. The
// timeline is the record of what a session did; this is the record of how it
// stopped, so anything listing sessions later does not have to guess. It is
// never rewritten: the first conclusion is the one that happened.
//
// The caller must not hold s.mu, and a failure to write never fails the session.
func (s *InfaiAgentSession) recordSessionConclusion(status contracts.SessionStatus, reason string) {
	s.mu.Lock()
	if s.meta.Conclusion != nil {
		s.mu.Unlock()
		return
	}
	s.meta.Conclusion = &store.SessionConclusion{Status: status, Reason: reason}
	s.meta.UpdatedAt = time.Now().UTC()
	meta := s.meta
	s.mu.Unlock()

	if err := s.store.SaveMeta(meta); err != nil {
		s.l.Error("persist session conclusion", "session_id", meta.ID, "status", status, "error", err)
	}
}

// publish hands one event to the session's hub.
//
// Callers must not hold s.mu: the hub takes it to fan an event out, so a send
// under the lock can deadlock against a bus that has filled up. The send also
// gives up once the session is canceled, so a publisher can never outlive it.
func (s *InfaiAgentSession) publish(event contracts.EventStream) {
	select {
	case s.eventBus <- event:
	case <-s.ctx.Done():
	}
}

// subscriber is one attached read-only client. It owns a bounded queue that
// the client's own streaming goroutine drains, so the session never blocks on
// a client.
type subscriber struct {
	events chan contracts.EventStream
}

// offer queues event without blocking, reporting false when the client is too
// slow to keep up.
func (sub *subscriber) offer(event contracts.EventStream) bool {
	select {
	case sub.events <- event:
		return true
	default:
		return false
	}
}

// evict drops the backlog, tells the client why it is being disconnected, and
// closes the queue so its streaming goroutine returns. The dropped events are
// reconstructible from a fresh SessionView, so nothing is lost permanently.
func (sub *subscriber) evict() {
	for {
		select {
		case <-sub.events:
		default:
			message := "subscriber fell behind live session events; rejoin for an authoritative snapshot"
			sub.events <- contracts.EventStream{
				Kind:      contracts.EventSubscriberGap,
				Timestamp: time.Now().UTC(),
				Content:   &message,
			}
			close(sub.events)
			return
		}
	}
}

// notifySubscribers fans one session event out to every attached client.
//
// The caller must hold s.mu. That is what makes a join atomic: a client either
// sees a state change in its SessionView or receives the event, never both and
// never neither. It is also what keeps these sends from racing the close in
// unsubscribe, so a send can never reach a closed channel.
func (s *InfaiAgentSession) notifySubscribers(event contracts.EventStream) {
	for sub := range s.subscribers {
		if sub.offer(event) {
			continue
		}
		delete(s.subscribers, sub)
		sub.evict()
	}
}
