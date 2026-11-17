// task_metadata.go：更新任务列表级元数据（名称/置顶/审核开关）的 HTTP 端点。
// 主要函数/类型：
//   - updateTaskMetadata：PATCH 任务 name/pinned/review_auto/review_enabled，关审核前须清空待审队列。

package server

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"artex/db"
)

const (
	maxTaskNameRunes           = 200
	maxTaskMetadataRequestSize = 16 << 10
)

// updateTaskMetadata changes list-only task metadata. It intentionally does not
// touch lifecycle state, scheduling, or the task's immutable description/goal.
func (s *Server) updateTaskMetadata(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if _, ok := s.m.Task(taskID); !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	var request struct {
		Name          *string `json:"name"`
		Pinned        *bool   `json:"pinned"`
		ReviewAuto    *bool   `json:"review_auto"`    // 【二开·P-REVIEW·D2b】自动审核开关
		ReviewEnabled *bool   `json:"review_enabled"` // 【二开·P-REVIEW·D2c】任务级审核总开关（m03934）
	}
	if !decodeJSON(w, r, &request, maxTaskMetadataRequestSize) {
		return
	}
	if request.Name == nil && request.Pinned == nil && request.ReviewAuto == nil && request.ReviewEnabled == nil {
		writeErr(w, http.StatusBadRequest, "Provide at least name, pinned, review_auto, or review_enabled")
		return
	}
	if request.Name != nil {
		name := strings.TrimSpace(*request.Name)
		if name == "" {
			writeErr(w, http.StatusBadRequest, "Task name is required")
			return
		}
		if utf8.RuneCountInString(name) > maxTaskNameRunes {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("Task name must not exceed %d characters", maxTaskNameRunes))
			return
		}
		request.Name = &name
	}
	// 【二开·审核流 §2.9.6】关闭审核前必须清空待审队列（pending）与待修订稿（rejected）：
	// 任一存在时返回 409 + 数量，要求先批准、修订重送或显式取消；否则 ReviewRunner 停用后
	// 会留下再也无法处置的孤儿稿。开启审核不校验（不回改已有 open，见 §10.1）。
	if request.ReviewEnabled != nil && !*request.ReviewEnabled {
		if t, ok := s.m.Task(taskID); ok && t.Store != nil {
			pend, _ := t.Store.ProposedIntents(1000)
			rej, _ := t.Store.RejectedIntents(1000)
			if len(pend)+len(rej) > 0 {
				writeErr(w, http.StatusConflict, fmt.Sprintf(
					"There are still %d pending-review and %d pending-revision intents. Approve them, revise and resubmit them, or explicitly cancel them (cancel_intent) before disabling review",
					len(pend), len(rej)))
				return
			}
		}
	}
	task, err := s.m.UpdateTaskMetadata(taskID, db.TaskPatch{Name: request.Name, Pinned: request.Pinned, ReviewAuto: request.ReviewAuto, ReviewEnabled: request.ReviewEnabled})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if task == nil {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, taskDTO(task, s.resolvedTaskStatus(task)))
}
