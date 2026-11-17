// cancelcause.go：可取消运行的命名中止原因——预置 cause 集与从 context 解析。
// 主要函数/类型：
//   - AbortCause / cause / Causef：带 code/short/text 的中止原因错误类型与构造器。
//   - AbortPausedByUser 等预置变量：任务级/工作级/对话级/进程级的全部内置中止原因。
//   - AbortReason：从已取消的 context 解析出命名中止原因。

package agent

import (
	"context"
	"errors"
	"fmt"
)

// AbortCause names why an agent run's context was cancelled. Every cancellation
// site should attach one so the activity trace can report the real initiator.
type AbortCause struct {
	Code  string
	Short string
	Text  string
}

func (c *AbortCause) Error() string { return c.Text }

func cause(code, short, text string) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: text}
}

// Causef builds a cause that includes runtime-specific detail.
func Causef(code, short, format string, args ...any) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: fmt.Sprintf(format, args...)}
}

var (
	// Task-level execution context.
	AbortPausedByUser = cause("paused_by_user", "User paused the task",
		"The user paused this task through POST /api/tasks/{id}/control (action=pause). This Planner/Worker run was cancelled. Running intents return to frontier (open) and will be claimed and executed again from the start after the task resumes")
	AbortPausedByOrchestrator = cause("paused_by_orchestrator", "Orchestration Agent paused the task",
		"The orchestration Agent called pause_task. This Planner/Worker run was cancelled; running intents return to frontier (open) and will run again after resuming")
	AbortTaskDeleted = cause("task_deleted", "Task deleted",
		"Task deletion is in progress (DELETE /api/tasks/{id}). The deletion barrier cancelled the running Planner, Workers, and Main Agent. Results from this run will no longer be used")
	AbortPausedOnReload = cause("paused_on_reload", "Backend restored the task's paused state",
		"At startup, the backend restored the task's persisted paused state. This run was cancelled; normally no Agent is running during recovery")
	AbortGoalMet = cause("goal_met", "Planner confirmed goal completion",
		"The Planner determined that the task goal was achieved, marked the task done, and cancelled remaining Workers. These intents are marked stopped, not failed")
	AbortSettleDrainTimeout = cause("settle_drain_timeout", "Task-timeout finalization wait expired",
		"After task timeout, running Workers were allowed to finalize gracefully, but the 90-second drain grace period expired. Runs were force-cancelled and intents marked exhausted; facts and assets already saved during finalization are retained")

	// Per-work context.
	AbortKilledByPlanner = cause("killed_by_planner", "Planner terminated this intent",
		"The Planner called kill_work to terminate this intent, usually because the direction diverged or no longer has value. The intent is marked stopped and will not be claimed again automatically")
	AbortWorkPausedByUser = cause("work_paused_by_user", "User paused this Worker intent",
		"The user paused the running Worker. This call was cancelled and the intent is paused. Registered intents, facts, findings, and activity records are retained; execution restarts from the beginning on resume")
	AbortWorkCancelledByUser = cause("work_cancelled_by_user", "User deleted this Worker intent",
		"The user deleted the running Worker. This call was cancelled. After the Worker exits its write section, the server applies the selected deletion mode: soft deletion retains all output; hard deletion cascades to the intent and downstream nodes supported only by it")
	AbortWorkFinished = cause("work_finished", "Worker completed; context released",
		"The Worker completed normally; the engine released its context in detachWork. This is normal completion. If shown as an interruption, cancellation raced with finalization")
	AbortPausedRaceGuard = cause("paused_race_guard", "New run rejected while task was paused",
		"While the task was paused, the engine refused a new execution context to prevent a claim/pause race from starting a Worker. The claimed intent returns to frontier")

	// Main Agent and standalone conversation contexts.
	AbortChatStoppedByUser = cause("chat_stopped_by_user", "User stopped this conversation turn",
		"The user clicked Stop to cancel the Main Agent or conversation Agent turn. Existing activity records are retained; another message can be sent")
	AbortChatPausedWithTask = cause("chat_paused_with_task", "Task pause interrupted Main Agent",
		"Pausing the task also cancelled the active Main Agent conversation. Activity records are retained; this message is not automatically replayed when the task resumes")
	AbortChatTurnFinished = cause("chat_turn_finished", "Turn completed; context released",
		"This turn completed normally; the server is releasing its context. If shown as an interruption, cancellation raced with finalization")

	// Process-level and per-run hard backstop.
	AbortShutdown = cause("shutdown", "Backend process is shutting down",
		"The backend received SIGINT or SIGTERM and is restarting, updating, or shutting down. All running Agents are cancelled. After restart, leftover running intents are reset to open and executed again")
	AbortRunHardTimeout = cause("run_hard_timeout", "Single-run hard timeout triggered",
		"The run exceeded its soft wall-clock budget and additional grace period. A model request or tool call likely failed to return, preventing normal finalization at a turn boundary. Inspect the last tool call that did not return")
)

// AbortReason resolves the named cause attached to a cancelled run context.
func AbortReason(ctx context.Context) (code, short, text string, ok bool) {
	c := context.Cause(ctx)
	if c == nil {
		return "", "", "", false
	}
	var ac *AbortCause
	if errors.As(c, &ac) {
		return ac.Code, ac.Short, ac.Text, true
	}
	switch {
	case errors.Is(c, context.DeadlineExceeded):
		return "deadline_exceeded", "Upstream context reached its deadline",
			"The upstream context reached its deadline without a named cause attached through WithTimeoutCause: " + c.Error(), true
	case errors.Is(c, context.Canceled):
		return "canceled_no_cause", "No named cancellation cause was attached",
			"The upstream context was cancelled without a named cause attached through context.WithCancelCause. Register the reason in agent/cancelcause.go and connect it at the cancellation point", true
	default:
		return "other", firstLine(c.Error(), 80), c.Error(), true
	}
}
