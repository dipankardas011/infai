package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/actuators"
	"github.com/dipankardas011/infai/pkg/agent/auditor"
	"github.com/dipankardas011/infai/pkg/agent/comms"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/delegate"
	harnessErr "github.com/dipankardas011/infai/pkg/agent/errors"
	"github.com/dipankardas011/infai/pkg/agent/memory"
	"github.com/google/uuid"
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

func (s *InfaiAgentSession) configureDelegationTools() {
	if s.meta.AgentKind != contracts.InteractiveAgent {
		return
	}
	if len(s.availableTools) == 0 {
		s.availableTools = []contracts.Tool{}
	}
	s.availableTools = append(s.availableTools, delegate.SpawnSidecarLoopTool(), delegate.SpawnBackgroundSidecarLoopTool())
}

func (s *InfaiAgentSession) GenToolCallDispatchHandler() func([]contracts.ToolCall) ([]contracts.ChatMessage, bool) {

	checkIfAllowedToolCall := func(tc contracts.ToolCall) bool {
		if len(s.availableTools) == 0 {
			return false
		}
		for _, tool := range s.availableTools {
			if tool.Name == string(tc.Function.Name) {
				return true
			}
		}
		return false
	}

	return func(tcs []contracts.ToolCall) ([]contracts.ChatMessage, bool) {
		toolMessages := make([]contracts.ChatMessage, 0, len(tcs))
		turnCanceled := false
		caller := delegate.Caller{
			SessionID: s.meta.ID,
			Comms:     s.aeComms,
			Cancelled: s.userCancellation,
		}
		foregroundIDs := []uuid.UUID{}
		foregroundCalls := []contracts.ToolCall{}

		for _, tc := range tcs {
			if turnCanceled {
				status := contracts.ToolExecutionDenied
				content := contracts.NewToolExecutionError(
					tc.Function.Name,
					"turn_canceled",
					"the turn was canceled by the user before this tool ran",
					contracts.ResponsibilityUser,
					nil,
				).Error()
				toolMessages = append(toolMessages, contracts.NewToolMessage(tc.ID, content, status))
				s.publish(contracts.EventStream{
					Kind:      contracts.EventToolResult,
					Timestamp: time.Now().UTC(),
					ToolResult: &contracts.ToolExecutionResult{
						Status:   status,
						CallID:   tc.ID,
						CallName: tc.Function.Name,
						Error:    content,
					},
				})
				continue
			}

			policy := s.auditorPolicy.Check(tc.Function.Name)
			if !checkIfAllowedToolCall(tc) {
				policy = auditor.DenyPolicy
			}

			s.l.DebugContext(s.ctx, "tool call received",
				"agent_id", s.meta.ID,
				"call_id", tc.ID,
				"tool", tc.Function.Name,
				"policy", policy.String(),
			)
			status := contracts.ToolExecutionSuccess
			content := ""

			switch policy {
			case auditor.DenyPolicy:
				s.l.InfoContext(s.ctx, "tool call denied",
					"agent_id", s.meta.ID,
					"call_id", tc.ID,
					"tool", tc.Function.Name,
				)
				status = contracts.ToolExecutionDenied
				content = "tool execution was denied by session policy"
				toolMessages = append(toolMessages, contracts.NewToolMessage(tc.ID, content, status))
				s.publish(contracts.EventStream{
					Kind:      contracts.EventToolResult,
					Timestamp: time.Now().UTC(),
					ToolResult: &contracts.ToolExecutionResult{
						Status:   status,
						CallID:   tc.ID,
						CallName: tc.Function.Name,
						Error:    content,
					},
				})
				continue

			case auditor.HumanPolicy:
				s.l.InfoContext(s.ctx, "tool call HITL",
					"agent_id", s.meta.ID,
					"call_id", tc.ID,
					"tool", tc.Function.Name,
				)

				if err := s.performHITL(s.ctx, s.meta.ID, tc); err != nil {
					if errors.Is(err, harnessErr.ErrTurnCanceled) {
						turnCanceled = true
						status = contracts.ToolExecutionDenied
						content = contracts.NewToolExecutionError(
							tc.Function.Name,
							"turn_canceled",
							"the turn was canceled by the user before this tool ran",
							contracts.ResponsibilityUser,
							err,
						).Error()
					} else if errors.Is(err, harnessErr.ErrApprovalDenied) {
						status = contracts.ToolExecutionDenied
					} else if errors.Is(err, context.Canceled) {
						status = contracts.ToolExecutionError
						content = "tool execution was canceled by the user before completion"
					} else {
						status = contracts.ToolExecutionError
					}
					if content == "" {
						content = err.Error()
					}
					s.publish(contracts.EventStream{
						Kind:      contracts.EventToolResult,
						Timestamp: time.Now().UTC(),
						ToolResult: &contracts.ToolExecutionResult{
							Status:   status,
							CallID:   tc.ID,
							CallName: tc.Function.Name,
							Error:    content,
						},
					})
					toolMessages = append(toolMessages, contracts.NewToolMessage(tc.ID, content, status))
					continue
				}

			case auditor.AllowPolicy:
				s.l.InfoContext(s.ctx, "tool call allowed",
					"agent_id", s.meta.ID,
					"call_id", tc.ID,
					"tool", tc.Function.Name,
				)
			}

			switch tc.Function.Name {
			case contracts.ReadTool:
				output, err := s.fileManager.ReadExecution(s.ctx, tc)

				result := contracts.ToolExecutionResult{
					Status:   contracts.ToolExecutionSuccess,
					CallID:   tc.ID,
					Output:   output,
					CallName: tc.Function.Name,
				}
				if err != nil {
					status = contracts.ToolExecutionError
					content = err.Error()
					result.Status = contracts.ToolExecutionError
					result.Error = content
				} else {
					content = output
				}

				s.publish(contracts.EventStream{
					Kind:       contracts.EventToolResult,
					Timestamp:  time.Now().UTC(),
					ToolResult: &result,
				})

			case contracts.WriteTool:
				output, err := s.fileManager.WriteExecution(s.ctx, tc)

				result := contracts.ToolExecutionResult{
					Status:   contracts.ToolExecutionSuccess,
					CallID:   tc.ID,
					Output:   output,
					CallName: tc.Function.Name,
				}
				if err != nil {
					status = contracts.ToolExecutionError
					content = err.Error()
					result.Status = contracts.ToolExecutionError
					result.Error = content
				} else {
					content = output
				}

				s.publish(contracts.EventStream{
					Kind:       contracts.EventToolResult,
					Timestamp:  time.Now().UTC(),
					ToolResult: &result,
				})

			case contracts.EditTool:
				output, err := s.fileManager.EditExecution(s.ctx, tc)

				result := contracts.ToolExecutionResult{
					Status:   contracts.ToolExecutionSuccess,
					CallID:   tc.ID,
					Output:   output,
					CallName: tc.Function.Name,
				}
				if err != nil {
					status = contracts.ToolExecutionError
					content = err.Error()
					result.Status = contracts.ToolExecutionError
					result.Error = content
				} else {
					content = output
				}

				s.publish(contracts.EventStream{
					Kind:       contracts.EventToolResult,
					Timestamp:  time.Now().UTC(),
					ToolResult: &result,
				})

			case contracts.GlobTool:
				output, err := s.fileManager.GlobExecution(s.ctx, tc)

				result := contracts.ToolExecutionResult{
					Status:   contracts.ToolExecutionSuccess,
					CallID:   tc.ID,
					Output:   output,
					CallName: tc.Function.Name,
				}
				if err != nil {
					status = contracts.ToolExecutionError
					content = err.Error()
					result.Status = contracts.ToolExecutionError
					result.Error = content
				} else {
					content = output
				}

				s.publish(contracts.EventStream{
					Kind:       contracts.EventToolResult,
					Timestamp:  time.Now().UTC(),
					ToolResult: &result,
				})

			case contracts.ListTool:
				output, err := s.fileManager.ListExecution(s.ctx, tc)

				result := contracts.ToolExecutionResult{
					Status:   contracts.ToolExecutionSuccess,
					CallID:   tc.ID,
					Output:   output,
					CallName: tc.Function.Name,
				}
				if err != nil {
					status = contracts.ToolExecutionError
					content = err.Error()
					result.Status = contracts.ToolExecutionError
					result.Error = content
				} else {
					content = output
				}

				s.publish(contracts.EventStream{
					Kind:       contracts.EventToolResult,
					Timestamp:  time.Now().UTC(),
					ToolResult: &result,
				})

			case contracts.SearchTool:
				output, err := s.fileManager.SearchExecution(s.ctx, tc)

				result := contracts.ToolExecutionResult{
					Status:   contracts.ToolExecutionSuccess,
					CallID:   tc.ID,
					Output:   output,
					CallName: tc.Function.Name,
				}
				if err != nil {
					status = contracts.ToolExecutionError
					content = err.Error()
					result.Status = contracts.ToolExecutionError
					result.Error = content
				} else {
					content = output
				}

				s.publish(contracts.EventStream{
					Kind:       contracts.EventToolResult,
					Timestamp:  time.Now().UTC(),
					ToolResult: &result,
				})

			case contracts.BashTool:
				output, err := s.fileManager.BashExecution(s.ctx, tc)

				result := contracts.ToolExecutionResult{
					Status:   contracts.ToolExecutionSuccess,
					CallID:   tc.ID,
					Output:   output,
					CallName: tc.Function.Name,
				}
				if err != nil {
					status = contracts.ToolExecutionError
					content = err.Error()
					result.Status = contracts.ToolExecutionError
					result.Error = content
				} else {
					content = output
				}

				s.publish(contracts.EventStream{
					Kind:       contracts.EventToolResult,
					Timestamp:  time.Now().UTC(),
					ToolResult: &result,
				})

			case contracts.ReadSkillTool:
				output, err := s.skillRegistry.LoadSkillExecution(s.ctx, tc)

				result := contracts.ToolExecutionResult{
					Status:   contracts.ToolExecutionSuccess,
					CallID:   tc.ID,
					CallName: tc.Function.Name,
					Output:   output,
				}
				eventResult := result
				if err != nil {
					status = contracts.ToolExecutionError
					content = err.Error()
					result.Status = contracts.ToolExecutionError
					result.Error = content
					eventResult.Error = content
					eventResult.Status = contracts.ToolExecutionError
				} else {
					content = output
				}

				s.publish(contracts.EventStream{
					Kind:       contracts.EventSkillLoad,
					Timestamp:  time.Now().UTC(),
					ToolResult: &eventResult,
				})

			case contracts.TaskChecklistTool:
				output, err := s.taskChecklist.TaskChecklistExecution(s.ctx, tc)

				result := contracts.ToolExecutionResult{
					Status:   contracts.ToolExecutionSuccess,
					CallID:   tc.ID,
					CallName: tc.Function.Name,
					Output:   output,
				}
				if err != nil {
					status = contracts.ToolExecutionError
					content = err.Error()
					result.Status = contracts.ToolExecutionError
					result.Error = content
				} else {
					content = output
				}

				s.publish(contracts.EventStream{
					Kind:       contracts.EventToolTaskCheckList,
					Timestamp:  time.Now().UTC(),
					ToolResult: &result,
				})
			case contracts.SpawnSidecarLoopTool, contracts.SpawnBackgroundSidecarLoopTool:
				sidecarID, err := delegate.RequestSidecar(s.ctx, caller, tc)
				if err != nil {
					status = contracts.ToolExecutionError
					content = err.Error()
					if errors.Is(err, harnessErr.ErrTurnCanceled) {
						turnCanceled = true
					}
				} else if tc.Function.Name == contracts.SpawnSidecarLoopTool {
					foregroundIDs = append(foregroundIDs, sidecarID)
					foregroundCalls = append(foregroundCalls, tc)
					continue
				} else {
					content = delegate.GraftedMessageForBackgroundSidecarLoop(sidecarID)
				}

				result := contracts.ToolExecutionResult{
					Status:   status,
					CallID:   tc.ID,
					CallName: tc.Function.Name,
				}
				if err != nil {
					result.Error = content
				} else {
					result.Output = content
				}
				s.publish(contracts.EventStream{
					Kind:       contracts.EventToolResult,
					Timestamp:  time.Now().UTC(),
					ToolResult: &result,
				})

			default:
				status = contracts.ToolExecutionError
				content = contracts.NewToolExecutionError(
					tc.Function.Name,
					"unknown_tool",
					"the requested tool is not available in this session",
					contracts.ResponsibilityAgent,
					nil,
				).Error()
				s.publish(contracts.EventStream{
					Kind:      contracts.EventToolResult,
					Timestamp: time.Now().UTC(),
					ToolResult: &contracts.ToolExecutionResult{
						Status:   status,
						CallID:   tc.ID,
						CallName: tc.Function.Name,
						Error:    content,
					},
				})
			}

			toolMessages = append(toolMessages, contracts.NewToolMessage(tc.ID, content, status))
		}

		if len(foregroundIDs) > 0 {
			answers, waitErr := delegate.WaitForSidecars(s.ctx, caller, foregroundIDs, func(response comms.DelegatedTaskResponse) {
				idx := slices.Index(foregroundIDs, response.From)
				if idx == -1 {
					return
				}
				call := foregroundCalls[idx]
				content, err := delegate.Answer(response)
				result := contracts.ToolExecutionResult{
					Status:   contracts.ToolExecutionSuccess,
					CallID:   call.ID,
					CallName: call.Function.Name,
					Output:   content,
				}
				if err != nil {
					result.Status = contracts.ToolExecutionError
					result.Output = ""
					result.Error = err.Error()
				}
				s.publish(contracts.EventStream{
					Kind:       contracts.EventToolResult,
					Timestamp:  time.Now().UTC(),
					ToolResult: &result,
				})
			})

			for i, sidecarID := range foregroundIDs {
				call := foregroundCalls[i]
				status := contracts.ToolExecutionSuccess
				content := ""

				if response, ok := answers[sidecarID]; ok {
					var err error
					content, err = delegate.Answer(response)
					if err != nil {
						status = contracts.ToolExecutionError
						content = err.Error()
					}
				} else {
					status = contracts.ToolExecutionError
					content = "the sidecar did not answer"
					if waitErr != nil {
						content = waitErr.Error()
						if errors.Is(waitErr, harnessErr.ErrTurnCanceled) {
							turnCanceled = true
						}
					}
				}

				if _, answered := answers[sidecarID]; !answered {
					s.publish(contracts.EventStream{
						Kind:      contracts.EventToolResult,
						Timestamp: time.Now().UTC(),
						ToolResult: &contracts.ToolExecutionResult{
							Status:   status,
							CallID:   call.ID,
							CallName: call.Function.Name,
							Error:    content,
						},
					})
				}
				toolMessages = append(toolMessages, contracts.NewToolMessage(call.ID, content, status))
			}
		}

		return toolMessages, turnCanceled
	}
}

func (s *InfaiAgentSession) performHITL(ctx context.Context, agentID uuid.UUID, call contracts.ToolCall) error {
	approvalID, err := uuid.NewV7()
	if err != nil {
		return err
	}

	fingerprintInput := approvalID.String() + s.meta.ID.String() + agentID.String() + call.ID + string(call.Function.Name) + call.Function.Arguments
	hash := sha256.Sum256([]byte(fingerprintInput))
	fingerprint := hex.EncodeToString(hash[:])

	request := contracts.ApprovalRequest{
		ID:          approvalID,
		SessionID:   s.meta.ID,
		ToolCall:    call,
		Fingerprint: fingerprint,
		CreatedAt:   time.Now().UTC(),
	}

	pending := &pendingApproval{
		request:  request,
		decision: make(chan contracts.ApprovalConclusion, 1),
	}

	s.mu.Lock()

	if len(s.userCancellation) > 0 {
		s.mu.Unlock()
		return harnessErr.ErrTurnCanceled
	}

	if s.pendingApproval != nil {
		s.mu.Unlock()
		return errors.New("another tool approval is already pending")
	}

	pending.request.SessionName = s.meta.Name
	request = pending.request
	s.pendingApproval = pending
	s.mu.Unlock()

	s.publish(contracts.EventStream{Kind: contracts.EventApprovalRequested, Timestamp: time.Now().UTC(), HITLCall: &request})

	s.l.InfoContext(ctx, "tool approval requested",
		"approval_id", request.ID,
		"session_id", request.SessionID,
		"tool", request.ToolCall.Function.Name,
	)

	var decision contracts.ApprovalConclusion
	select {
	case decision = <-pending.decision:
	case <-ctx.Done():
		s.mu.Lock()
		if s.pendingApproval != pending {
			s.mu.Unlock()
			decision = <-pending.decision
			break
		}
		s.pendingApproval = nil
		s.mu.Unlock()

		s.publish(contracts.EventStream{
			Kind:      contracts.EventApprovalResolved,
			Timestamp: time.Now().UTC(),
			HITLCall:  &request,
			HITLResult: &contracts.ApprovalConclusion{
				ReqID:       request.ID,
				Fingerprint: request.Fingerprint,
				Decision:    contracts.ApprovalDeny,
				Reason:      "session canceled: " + context.Cause(ctx).Error(),
			},
		})
		return ctx.Err()
	}

	s.l.InfoContext(ctx, "tool approval resolved",
		"approval_id", request.ID,
		"decision", decision.Decision,
	)
	if decision.Decision != contracts.ApprovalApprove {
		if decision.Reason == userCanceledApprovalReason {
			return harnessErr.ErrTurnCanceled
		}
		return harnessErr.ErrApprovalDenied
	}
	return nil
}
