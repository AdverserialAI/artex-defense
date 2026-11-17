// intent_intervention.go：向已暂停的 Worker 意图注入人工后续消息的接口。
// 主要函数/类型：
//   - sendWorkerMessage：校验 request_id 与消息体，经 resume-from-transcript 路径把人工消息注入暂停的意图，并在独立 goroutine 中立即续跑。
//   - validWorkerMessageRequestID：校验 request_id 的字符集与长度。

package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"artex/db"
)

const maxWorkerMessageBytes = 64 << 10

func validWorkerMessageRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}

// sendWorkerMessage continues a paused Worker intent with a human-authored message.
// The message is injected as the next turn's input through the same
// resume-from-transcript path the worker uses on a normal resume (ExecuteWithMessage),
// so this reuses the existing pause/resume machinery rather than a bespoke protocol.
// The run happens in a dedicated goroutine outside the worker pool (runDetachedIntent),
// so the message is picked up immediately even when every pool slot is busy — the same
// way the main-agent chat handler starts its run directly. In-memory only: a process
// restart re-runs the intent from its transcript without the message, which is
// acceptable for this rare interrupt-then-continue action.
func (s *Server) sendWorkerMessage(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	iid, err := strconv.ParseInt(r.PathValue("iid"), 10, 64)
	if err != nil || iid <= 0 {
		writeErr(w, http.StatusBadRequest, "bad intent id")
		return
	}
	var req struct {
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	}
	if !decodeJSON(w, r, &req, maxWorkerMessageBytes) {
		return
	}
	message := strings.TrimSpace(req.Message)
	requestID := strings.TrimSpace(req.RequestID)
	if message == "" {
		writeErr(w, http.StatusBadRequest, "Message is required")
		return
	}
	if len([]rune(message)) > 4000 {
		writeErr(w, http.StatusBadRequest, "Message must not exceed 4000 characters")
		return
	}
	if !validWorkerMessageRequestID(requestID) {
		writeErr(w, http.StatusBadRequest, "request_id must contain 1–128 letters, digits, -, _, ., or :")
		return
	}

	// Reject non-runnable task lifecycles up front so the caller gets a clear reason
	// instead of a silent no-op. The intent itself must be paused: the UI flow is
	// interrupt (pause) first, then send.
	if s.engine.IsDeleting(t.ID) {
		writeErr(w, http.StatusConflict, "Task is being deleted; messages cannot be sent to the Worker")
		return
	}
	lifecycle := t.lifecycleSnapshot()
	switch {
	case lifecycle.Paused || s.engine.IsPaused(t.ID):
		writeErr(w, http.StatusConflict, "Task is paused. Resume it before messaging the Worker")
		return
	case lifecycle.Queued:
		writeErr(w, http.StatusConflict, "Messages cannot be sent to Workers in queued tasks")
		return
	case isTerminalStatus(lifecycle.Status):
		writeErr(w, http.StatusConflict, "Messages cannot be sent to Workers in terminal tasks")
		return
	case s.engine.isSettling(t.ID):
		writeErr(w, http.StatusConflict, "Task is finalizing; messages cannot be sent to the Worker")
		return
	}

	node, err := t.Store.GetNode(iid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if node == nil {
		if inherited, sourceErr := t.Store.GetNodeWithSources(iid); sourceErr == nil && inherited != nil && inherited.Inherited {
			writeErr(w, http.StatusConflict, "Inherited intents are read-only; Worker messages cannot be sent")
			return
		}
		writeErr(w, http.StatusNotFound, "intent not found")
		return
	}
	if node.Kind != db.KindIntent {
		writeErr(w, http.StatusConflict, "node is not an intent")
		return
	}
	if node.State != "paused" {
		writeErr(w, http.StatusConflict, "Only paused Workers can receive messages. Pause the Worker first")
		return
	}
	agentMessage, ok := s.prepareChatMentionMessage(w, message)
	if !ok {
		return
	}

	// runDetachedIntent transitions paused->running, emits the user turn and starts a
	// dedicated run. Root the run at s.ctx so a disconnected browser cannot strand it
	// while task pause/delete/shutdown still stop it.
	if err := s.engine.runDetachedIntent(s.ctx, t, iid, requestID, message, agentMessage); err != nil {
		switch {
		case errors.Is(err, db.ErrIntentStateConflict):
			writeErr(w, http.StatusConflict, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":         iid,
		"state":      "running",
		"accepted":   true,
		"request_id": requestID,
	})
}
