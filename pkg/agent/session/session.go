package session

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/actuators"
	"github.com/dipankardas011/infai/pkg/agent/agent"
	"github.com/dipankardas011/infai/pkg/agent/auditor"
	"github.com/dipankardas011/infai/pkg/agent/comms"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	harnessErr "github.com/dipankardas011/infai/pkg/agent/errors"
	"github.com/dipankardas011/infai/pkg/agent/memory"
	"github.com/dipankardas011/infai/pkg/agent/models"
	"github.com/dipankardas011/infai/pkg/agent/prompts"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/dipankardas011/infai/pkg/agent/vision"
	"github.com/google/uuid"
)

type InfaiAgentSession struct {
	l  *slog.Logger
	mu sync.Mutex

	// Lifecycle: derived from the engine's context, ended by Close or by a
	// fatal persistence error.
	ctx       context.Context
	cancel    context.CancelCauseFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeDone chan struct{}

	// Durable state: the on-disk timeline and its lightweight session index.
	meta     store.SessionMeta
	timeline *store.Timeline
	store    *store.SessionStore

	// Session state visible to joined clients. status mirrors the status the
	// agent reports for itself; the session's own operations (compacting,
	// switching branches, waiting on approval) set it directly, and every one
	// of them runs while the agent cannot report a status of its own.
	// activeTimeline holds only messages already committed to the timeline,
	// inFlight holds the events of the generation still in progress, and the
	// two together are everything a joining client needs to render the session.
	status         contracts.SessionStatus
	fatalErr       error
	inFlight       []contracts.EventStream
	activeTimeline []contracts.ChatMessage

	// Decisions the session is waiting on: a tool approval from the user, or
	// the agent adopting a branch point on its next write.
	pendingApproval     *pendingApproval
	pendingBranchParent uuid.UUID

	// The principal agent and the model it runs on.
	agent *agent.Agent
	model contracts.InfaiModelAdaptor

	// Capabilities the agent runs with.
	auditorPolicy   *auditor.AuditorPolicy
	availableTools  []contracts.Tool
	availableSkills []contracts.Skill
	fileManager     *actuators.FileManager
	skillRegistry   *memory.SkillRegistry
	taskChecklist   *memory.TaskChecklist

	// Observation: the agent's live events and the clients joined to them.
	eventBus    chan contracts.EventStream
	subscribers map[*subscriber]struct{}

	// Peer messaging, carried but not yet consumed.
	aeComms *comms.ISACChannel
}

type pendingApproval struct {
	request  contracts.ApprovalRequest
	decision chan contracts.ApprovalConclusion
}

func NewSession(
	engineCtx context.Context,
	id uuid.UUID,
	l *slog.Logger,
	chosenModel contracts.ProvisionedModel,
	cwd string,
	ss *store.SessionStore,
	aeComms *comms.ISACChannel,
) (*InfaiAgentSession, error) {
	model, err := models.ProvisionModelClient(chosenModel)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	meta := store.SessionMeta{
		ID:        id,
		Provider:  model.GetModelSpecs().ProviderName(),
		Model:     model.GetModelSpecs().Model().Id,
		Cwd:       cwd,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := ss.SaveMeta(meta); err != nil {
		return nil, err
	}
	timeline, err := ss.LoadSessionTimelineClient(id)
	if err != nil {
		return nil, err
	}

	sess, err := newRuntimeSession(engineCtx, l, model, meta, nil, timeline, ss, aeComms)
	if err != nil {
		_ = timeline.Close()
		return nil, err
	}
	return sess, nil
}

func NewResumedSession(
	engineCtx context.Context,
	l *slog.Logger,
	chosenModel contracts.ProvisionedModel,
	meta store.SessionMeta,
	history []contracts.ChatMessage,
	timeline *store.Timeline,
	sessionStore *store.SessionStore,
	aeComms *comms.ISACChannel,
) (*InfaiAgentSession, error) {
	model, err := models.ProvisionModelClient(chosenModel)
	if err != nil {
		return nil, err
	}
	return newRuntimeSession(engineCtx, l, model, meta, history, timeline, sessionStore, aeComms)
}

func newRuntimeSession(
	engineCtx context.Context,
	l *slog.Logger,
	model contracts.InfaiModelAdaptor,
	meta store.SessionMeta,
	history []contracts.ChatMessage,
	timeline *store.Timeline,
	sessionStore *store.SessionStore,
	aeComms *comms.ISACChannel,
) (*InfaiAgentSession, error) {
	if engineCtx == nil {
		return nil, errors.New("session: engine context is required")
	}
	ctx, cancel := context.WithCancelCause(engineCtx)
	s := &InfaiAgentSession{
		l:              l,
		ctx:            ctx,
		cancel:         cancel,
		closeDone:      make(chan struct{}),
		meta:           meta,
		status:         contracts.SessionIdle,
		model:          model,
		timeline:       timeline,
		store:          sessionStore,
		auditorPolicy:  auditor.NewAuditorPolicy(),
		taskChecklist:  memory.NewTaskChecklist(),
		activeTimeline: append([]contracts.ChatMessage(nil), history...),
		eventBus:       make(chan contracts.EventStream, 256),
		subscribers:    make(map[*subscriber]struct{}),
		aeComms:        aeComms,
	}

	var err error
	s.fileManager, err = actuators.NewFileManager(meta.Cwd)
	if err != nil {
		cancel(err)
		return nil, fmt.Errorf("session workspace: %w", err)
	}
	s.meta.Cwd = s.fileManager.Root()
	if err := sessionStore.SaveMeta(s.meta); err != nil {
		cancel(err)
		return nil, err
	}

	s.configureFileTools()
	s.skillRegistry, err = memory.LoadSkillRegistry(s.meta.Cwd)
	if err != nil {
		cancel(err)
		return nil, fmt.Errorf("load skill registry: %w", err)
	}
	s.configureMemoryTools()

	systemPrompt, err := prompts.GetBasicSystemPrompt(s.availableTools, s.availableSkills, s.meta.Cwd)
	if err != nil {
		cancel(err)
		return nil, err
	}

	s.agent, err = agent.NewAgent(
		s.model,
		contracts.InteractiveAgent,
		s.commitMessages,
		s.eventBus,
		s.GenToolCallDispatchHandler(s.ctx),
		systemPrompt,
		agent.WithMaxTurns(1000),
		agent.WithTools(s.availableTools...),
		agent.WithAutoCompaction(s.shouldCompact, s.autoCompact),
	)
	if err != nil {
		cancel(err)
		return nil, err
	}

	state, err := getLatestTaskChecklist(s.timeline, s.timeline.CurrentHeadEventID())
	if err != nil {
		cancel(err)
		return nil, fmt.Errorf("reconstruct task checklist: %w", err)
	}
	if err := s.taskChecklist.Restore(state); err != nil {
		cancel(err)
		return nil, fmt.Errorf("restore task checklist: %w", err)
	}

	s.wg.Go(func() {
		s.handlerForSessionEvents(s.ctx)
	})
	s.wg.Go(func() {
		s.agent.StartLoop(s.ctx, s.activeTimeline)
	})

	return s, nil
}

func (s *InfaiAgentSession) Meta() store.SessionMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta
}

func (s *InfaiAgentSession) Status() contracts.SessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *InfaiAgentSession) ViewTimeline() ([]store.Event, uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, err := s.timeline.LoadEntireTimeline()
	head := s.timeline.CurrentHeadEventID()
	if s.pendingBranchParent != uuid.Nil {
		head = s.pendingBranchParent
	}
	return events, head, err
}

func (s *InfaiAgentSession) SelectBranch(eventID uuid.UUID) (contracts.TaskChecklistState, error) {
	s.mu.Lock()
	if s.status != contracts.SessionIdle {
		s.mu.Unlock()
		return contracts.TaskChecklistState{}, fmt.Errorf("session must be idle before selecting a branch")
	}
	if !s.agent.MailboxEmpty() {
		s.mu.Unlock()
		return contracts.TaskChecklistState{}, errors.New("session has queued messages")
	}
	s.mu.Unlock()

	if _, err := s.timeline.LoadEvent(eventID); err != nil {
		return contracts.TaskChecklistState{}, err
	}

	checklist, err := getLatestTaskChecklist(s.timeline, eventID)
	if err != nil {
		return contracts.TaskChecklistState{}, err
	}

	// Validated before anything is changed, because a corrupted checklist must
	// not leave the session half-switched onto the branch.
	if err := memory.ValidateTaskChecklistState(checklist); err != nil {
		return contracts.TaskChecklistState{}, err
	}

	encoded, err := json.Marshal(checklist)
	if err != nil {
		return contracts.TaskChecklistState{}, fmt.Errorf("encode task checklist for clients: %w", err)
	}
	payload := string(encoded)

	events, err := s.timeline.LoadActiveContextAt(eventID)
	if err != nil {
		return contracts.TaskChecklistState{}, err
	}

	history, err := TimelineHistory(s.timeline, events)
	if err != nil {
		return contracts.TaskChecklistState{}, err
	}

	if err := s.agent.ReplaceHistory(s.ctx, history); err != nil {
		return contracts.TaskChecklistState{}, err
	}

	s.mu.Lock()
	if !s.agent.MailboxEmpty() {
		s.mu.Unlock()
		return contracts.TaskChecklistState{}, errors.New("session received a queued message while switching branches")
	}
	if err := s.taskChecklist.Restore(checklist); err != nil {
		s.mu.Unlock()
		return contracts.TaskChecklistState{}, err
	}
	s.pendingBranchParent = eventID
	s.activeTimeline = append([]contracts.ChatMessage(nil), history...)
	s.mu.Unlock()

	// The checklist now belongs to the branch, and a joined client renders it
	// from this event, so tell them rather than let the header go stale.
	s.publish(contracts.EventStream{Kind: contracts.EventToolTaskCheckList, Timestamp: time.Now().UTC(), Content: &payload})
	return checklist, nil
}

func (s *InfaiAgentSession) Rename(name string) error {
	s.mu.Lock()

	switch s.status {
	case contracts.SessionCompleted, contracts.SessionMaxIterationExhausted, contracts.SessionTombstone:
		fatalErr := s.fatalErr
		s.mu.Unlock()
		if fatalErr != nil {
			return fmt.Errorf("session is concluded: %w", fatalErr)
		}
		return errors.New("session is concluded")
	}

	s.meta.Name = name
	s.meta.UpdatedAt = time.Now().UTC()
	meta := s.meta
	s.mu.Unlock()
	return s.store.SaveMeta(meta)
}

func (s *InfaiAgentSession) EnqueueUserMessage(ctx context.Context, input contracts.UserInput) error {
	if input.Empty() {
		return fmt.Errorf("%w: message is required", harnessErr.ErrInvalidInput)
	}
	images, err := vision.ValidateInputs(input.Images)
	if err != nil {
		return fmt.Errorf("%w: %v", harnessErr.ErrInvalidInput, err)
	}
	input.Images = images

	s.mu.Lock()

	switch s.status {
	case contracts.SessionCompacting:
		s.mu.Unlock()
		return errors.New("session is compacting")
	case contracts.SessionCompleted, contracts.SessionMaxIterationExhausted, contracts.SessionTombstone:
		fatalErr := s.fatalErr
		s.mu.Unlock()
		if fatalErr != nil {
			return fmt.Errorf("session is concluded: %w", fatalErr)
		}
		return errors.New("session is concluded")
	default:
		select {
		case <-s.ctx.Done():
			s.mu.Unlock()
			return context.Cause(s.ctx)
		default:
		}

		if err := contracts.ValidateUserInput(s.model.GetModelSpecs().Model(), input); err != nil {
			s.mu.Unlock()
			return fmt.Errorf("%w: %v", harnessErr.ErrInvalidInput, err)
		}

		select {
		case <-ctx.Done():
			s.mu.Unlock()
			return ctx.Err()
		default:
		}
	}

	if s.meta.Name == "" {
		s.meta.Name = sessionNameFromPrompt(input.Text)
		s.meta.UpdatedAt = time.Now().UTC()
	}
	meta := s.meta
	s.mu.Unlock()

	if err := s.store.SaveMeta(meta); err != nil {
		s.l.Error("persist session metadata", "session_id", meta.ID, "error", err)
	}

	if err := s.agent.Enqueue(ctx, contracts.NewUserMessageWithInput(input)); err != nil {
		return err
	}
	return nil
}

func (s *InfaiAgentSession) ResolveApproval(id uuid.UUID, decision contracts.ApprovalConclusion) error {
	s.mu.Lock()

	switch s.status {
	case contracts.SessionCompacting:
		s.mu.Unlock()
		return errors.New("session is compacting")
	case contracts.SessionCompleted, contracts.SessionMaxIterationExhausted, contracts.SessionTombstone:
		fatalErr := s.fatalErr
		s.mu.Unlock()
		if fatalErr != nil {
			return fmt.Errorf("session is concluded: %w", fatalErr)
		}
		return errors.New("session is concluded")

	default:
		if s.pendingApproval == nil || s.pendingApproval.request.ID != id {
			s.mu.Unlock()
			return errors.New("approval not found or already resolved")
		}

		if subtle.ConstantTimeCompare([]byte(s.pendingApproval.request.Fingerprint), []byte(decision.Fingerprint)) != 1 {
			s.mu.Unlock()
			return errors.New("invalid approval fingerprint")
		}

		if decision.Decision != contracts.ApprovalApprove && decision.Decision != contracts.ApprovalDeny && decision.Decision != contracts.ApprovalDenyWithReason {
			s.mu.Unlock()
			return errors.New("invalid approval decision")
		}
	}

	pending := s.pendingApproval
	s.pendingApproval = nil
	request := pending.request
	s.mu.Unlock()

	// The decision is validated and released here; moving the session's status
	// is the hub's decision, taken from the event.
	s.publish(contracts.EventStream{Kind: contracts.EventApprovalResolved, Timestamp: time.Now().UTC(), HITLCall: &request, HITLResult: &decision})
	pending.decision <- decision

	return nil
}

func (s *InfaiAgentSession) handlerForSessionEvents(ctx context.Context) {
	for {
		var event contracts.EventStream

		select {
		case <-ctx.Done():
			return
		case event = <-s.eventBus:
		}

		switch event.Kind {
		case contracts.DeltaContent,
			contracts.DeltaReasoning,
			contracts.EventProviderEvent,
			contracts.EventToolCall,
			contracts.EventToolResult,
			contracts.EventToolTaskCheckList,
			contracts.EventSkillLoad,
			contracts.EventMessageFromAgentInbox,
			contracts.NotifyAgentUsage,
			contracts.EventApprovalCanceled:
			s.mu.Lock()
			s.inFlight = append(s.inFlight, event)
			s.notifySubscribers(event)
			s.mu.Unlock()

		case contracts.EventSessionTransitionState:
			// The agent reports the eventStatus of its own loop. A runnable report
			// must not end an operation the session is running: the agent
			// publishes idle just before it parks, so that report can arrive
			// after the session has already moved on.
			eventStatus := contracts.SessionStatus(*event.Content)

			s.mu.Lock()
			if eventStatus == contracts.SessionIdle || eventStatus == contracts.SessionBusy {
				if s.status == contracts.SessionIdle || s.status == contracts.SessionBusy {
					s.status = eventStatus
				}
			} else {
				s.status = eventStatus
			}
			s.inFlight = append(s.inFlight, event)
			s.notifySubscribers(event)
			s.mu.Unlock()

			// The loop is not coming back from here, so this is how the session
			// ended. The status says why on its own, so no reason is recorded.
			if eventStatus == contracts.SessionCompleted || eventStatus == contracts.SessionMaxIterationExhausted {
				s.recordSessionConclusion(eventStatus, "")
			}

		case contracts.EventManualCompactionTriggered,
			contracts.EventAutoCompactionTriggered:
			s.mu.Lock()
			s.status = contracts.SessionCompacting
			s.inFlight = append(s.inFlight, event)
			s.notifySubscribers(event)
			s.mu.Unlock()

		case contracts.CompactionSummary:
			// A compaction is over and its continuation is installed, so the
			// session is runnable again. An automatic compaction is followed by
			// the agent's next busy report, which is what resumes that turn.
			s.mu.Lock()
			s.status = contracts.SessionIdle
			s.inFlight = append(s.inFlight, event)
			s.notifySubscribers(event)
			s.mu.Unlock()

		case contracts.EventApprovalRequested:
			// The approval request itself is in the joined view; the status says
			// the session is not runnable until it is resolved.
			s.mu.Lock()
			s.status = contracts.SessionWaitingApproval
			s.inFlight = append(s.inFlight, event)
			s.notifySubscribers(event)
			s.mu.Unlock()

		case contracts.EventApprovalResolved:
			// Resolving an approval only releases the waiting tool call. The
			// agent is the one that reports the session busy again, so that is
			// the status the resolution moves to.
			s.mu.Lock()
			s.status = contracts.SessionBusy
			s.inFlight = append(s.inFlight, event)
			s.notifySubscribers(event)
			s.mu.Unlock()

		case contracts.EventSessionFatal:
			reason := "session concluded"
			if event.Content != nil {
				reason = *event.Content
			}
			cause := errors.New(reason)

			// The reason reaches the clients before the context dies, because
			// the cancellation is what ends this loop.
			s.mu.Lock()
			s.fatalErr = cause
			s.status = contracts.SessionTombstone
			s.inFlight = append(s.inFlight, event)
			s.notifySubscribers(event)
			s.mu.Unlock()
			s.cancel(cause)

			s.recordSessionConclusion(contracts.SessionTombstone, reason)

		default:
			s.l.Warn("session received an event no case handles so dropping it", "session_id", s.meta.ID, "kind", event.Kind)
		}
	}
}

// commitMessages durably appends one batch of agent messages and only then
// exposes them in the session's committed history. The agent loop calls it
// through its commit callback and blocks until it returns. A durable write
// failure is fatal to the session, so the write path owns that transition.
func (s *InfaiAgentSession) commitMessages(ctx context.Context, messages []contracts.ChatMessage) error {
	if len(messages) == 0 {
		return nil
	}

	s.mu.Lock()
	branchParent := s.pendingBranchParent
	s.mu.Unlock()

	for i := range messages {
		message := messages[i]
		record := store.Record{Kind: store.KindMessage, Timestamp: time.Now().UTC(), Message: &message}

		var err error
		// The first message after a branch selection re-parents itself onto the
		// selected event; every later message walks forward from it.
		if i == 0 && branchParent != uuid.Nil {
			_, err = s.timeline.BranchFromEventID(record, branchParent)
		} else {
			_, err = s.timeline.AppendToHead(record)
		}
		if err != nil {
			commitErr := fmt.Errorf("persist message: %w", err)
			s.l.ErrorContext(ctx, "session could not persist its timeline", "session_id", s.meta.ID, "error", err)

			reason := commitErr.Error()
			s.publish(contracts.EventStream{Kind: contracts.EventSessionFatal, Timestamp: time.Now().UTC(), Content: &reason})

			return commitErr
		}
	}

	s.mu.Lock()
	s.activeTimeline = append(s.activeTimeline, messages...)
	if branchParent != uuid.Nil && s.pendingBranchParent == branchParent {
		s.pendingBranchParent = uuid.Nil
	}
	// Everything a client has been sent up to here is durable now and part of
	// activeTimeline, so the in-flight log starts empty and a client that joins
	// later reads the same content out of history instead.
	s.inFlight = s.inFlight[:0]
	s.meta.UpdatedAt = time.Now().UTC()
	meta := s.meta
	s.mu.Unlock()

	if err := s.store.SaveMeta(meta); err != nil {
		s.l.Error("persist session metadata", "session_id", meta.ID, "error", err)
	}
	return nil
}

// commitCompaction durably records a checkpoint and replaces the committed
// history with the continuation the agent should continue from.
func (s *InfaiAgentSession) commitCompaction(commit compactionCommit) error {
	if _, err := s.timeline.AppendToHead(store.Record{
		Kind:      store.KindCompaction,
		Timestamp: time.Now().UTC(),
		Compaction: &store.CompactionRecord{
			Summary:       commit.Summary,
			TaskChecklist: &commit.TaskChecklist,
		},
	}); err != nil {
		return fmt.Errorf("persist compaction: %w", err)
	}

	s.mu.Lock()
	s.activeTimeline = append([]contracts.ChatMessage(nil), commit.History...)
	s.inFlight = s.inFlight[:0]
	s.mu.Unlock()
	return nil
}

func (s *InfaiAgentSession) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		// Teardown leaves the concluded status readable, so a client that asks
		// after the session is gone still learns it is gone. The hub is already
		// stopping, so this is the one status the hub does not write.
		s.status = contracts.SessionTombstone
		s.mu.Unlock()
		// A session that already concluded keeps the conclusion it recorded;
		// one that is only being closed records that, which is what happened.
		s.recordSessionConclusion(contracts.SessionTombstone, "session closed")
		s.cancel(harnessErr.ErrSessionClosed)
		s.wg.Wait()

		s.mu.Lock()
		for sub := range s.subscribers {
			delete(s.subscribers, sub)
			close(sub.events)
		}
		s.mu.Unlock()
		if s.timeline != nil {
			_ = s.timeline.Close()
		}
		close(s.closeDone)
	})
	<-s.closeDone
}

func TimelineHistory(timeline *store.Timeline, events []store.Event) ([]contracts.ChatMessage, error) {
	records, err := CompleteResolveRawTimelineEventsToRecords(timeline, events)
	if err != nil {
		return nil, err
	}
	var history []contracts.ChatMessage
	for _, record := range records {
		switch record.Kind {
		case store.KindMessage:
			if record.Message != nil {
				history = append(history, *record.Message)
			}
		case store.KindCompaction:
			if record.Compaction == nil {
				continue
			}
			checklist := contracts.TaskChecklistState{Items: []contracts.TaskChecklistItem{}}
			if record.Compaction.TaskChecklist != nil {
				checklist = *record.Compaction.TaskChecklist
			}
			if err := memory.ValidateTaskChecklistState(checklist); err != nil {
				return nil, fmt.Errorf("invalid compacted task checklist: %w", err)
			}
			checklistContext, err := taskChecklistContextForCompaction(checklist)
			if err != nil {
				return nil, err
			}
			content, err := continuationContext(record.Compaction.Summary, checklistContext)
			if err != nil {
				return nil, err
			}
			history = append(history, contracts.NewUserMessage(content))
		}
	}
	return history, nil
}

func CompleteResolveRawTimelineEventsToRecords(timeline *store.Timeline, events []store.Event) ([]store.Record, error) {
	records := make([]store.Record, 0, len(events))
	for _, event := range events {
		record := event.Record
		if record == nil {
			resolved, err := timeline.ResolveRecord(event)
			if err != nil {
				return nil, err
			}
			record = &resolved
		}
		records = append(records, *record)
	}
	return records, nil
}

func sessionNameFromPrompt(prompt string) string {
	const maxRunes = 56
	title := strings.Join(strings.Fields(prompt), " ")
	if title == "" {
		return "Untitled session"
	}
	runes := []rune(title)
	if len(runes) <= maxRunes {
		return title
	}
	cut := maxRunes
	for i := maxRunes; i > maxRunes/2; i-- {
		if runes[i] == ' ' {
			cut = i
			break
		}
	}
	return strings.TrimSpace(string(runes[:cut])) + "…"
}
