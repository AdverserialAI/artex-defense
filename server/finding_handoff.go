// finding_handoff.go：把同一资产的新 Finding 追加进既有内网渗透任务的移交工具。
// 主要函数/类型：
//   - toolAppendFindingToTask：append_finding_to_task 工具，幂等地把 Finding 内容与证据补进目标任务。
//   - findingHandoffText：生成带持久化标记的 Finding 移交文本（含来源、摘要与证据）。
//   - findingHandoffMarker / clipFindingHandoff：移交标记生成 / 移交文本裁剪。

package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"artex/agent"
	"artex/db"
	actool "norma/tool"
)

func findingHandoffMarker(findingID int64) string {
	return fmt.Sprintf("[dispatcher_finding_id=%d]", findingID)
}

func clipFindingHandoff(text string, maxRunes int) string {
	r := []rune(strings.TrimSpace(text))
	if len(r) <= maxRunes {
		return string(r)
	}
	return string(r[:maxRunes]) + "…"
}

func findingHandoffText(f *db.DBFinding) string {
	var b strings.Builder
	marker := findingHandoffMarker(f.ID)
	b.WriteString(marker)
	b.WriteString("\nNew Finding for the same asset. Include it in the current internal penetration testing plan; do not create another subtask.\n")
	if f.TaskID != nil {
		fmt.Fprintf(&b, "Source task: #%d\n", *f.TaskID)
	}
	fmt.Fprintf(&b, "Finding：#%d · %s · %s\n", f.ID, f.VulnClass, f.Severity)
	if name := clipFindingHandoff(f.Name, 500); name != "" {
		b.WriteString("Name: " + name + "\n")
	}
	b.WriteString("Summary: " + clipFindingHandoff(f.Summary, 4000) + "\n")
	if evidence := clipFindingHandoff(f.Evidence, 8000); evidence != "" {
		b.WriteString("Evidence:\n" + evidence)
	}
	return b.String()
}

// toolAppendFindingToTask supplements the internal-pentest task already
// responsible for a finding's asset. A persisted marker makes retries
// idempotent; hints wake a running planner, while terminal tasks receive a new
// goal before shared task admission revives them.
func (s *Server) toolAppendFindingToTask() actool.CoreTool {
	return wrTool("append_finding_to_task",
		"Append a Finding's contents and evidence to the internal penetration-testing task responsible for the same asset. task_id is the internal task ID and finding_id is the Finding ID; target_asset is required for multi-host findings. Cross parent/child lineage is allowed, but the target task must register the same asset. Terminal tasks gain a new goal and resume; repeated appends return already_appended.",
		objSchema(map[string]any{
			"task_id":      strParam("ID of the internal penetration testing task responsible for the same asset"),
			"finding_id":   map[string]any{"type": "integer", "description": "ID of the Finding to append"},
			"target_asset": strParam("Optional: exact asset IP/domain/URL for a multi-host Finding"),
		}, "task_id", "finding_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				TaskID      string          `json:"task_id"`
				FindingID   json.RawMessage `json:"finding_id"`
				TargetAsset string          `json:"target_asset"`
			}
			_ = json.Unmarshal(in, &a)
			taskID := strings.TrimSpace(a.TaskID)
			findingID := parseProfileID(a.FindingID)
			if taskID == "" || findingID <= 0 {
				return actool.Errorf("task_id and a valid finding_id are required"), nil
			}
			target, ok := s.m.Task(taskID)
			if !ok || target == nil {
				return actool.Errorf("Subtask does not exist: " + taskID), nil
			}
			finding, err := s.m.pg.GetFinding(findingID)
			if err != nil {
				return actool.Errorf("Failed to read Finding: " + err.Error()), nil
			}
			if finding == nil {
				return actool.Errorf("Finding does not exist"), nil
			}
			if target.CategoryName != "内网渗透" {
				return actool.Errorf("Target task is not an internal penetration testing subtask"), nil
			}
			assetKeys, err := s.findingDispatchAssetKeys(findingID, a.TargetAsset)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("Failed to resolve Finding assets: %v", err)), nil
			}
			matchedAsset := ""
			for _, key := range assetKeys {
				var exists bool
				if err := s.m.pg.QueryRow(`SELECT EXISTS(SELECT 1 FROM task_dispatch_targets
					WHERE task_id=$1 AND asset_key=$2)`, target.ID, key).Scan(&exists); err != nil {
					return actool.Errorf(fmt.Sprintf("Failed to check target task assets: %v", err)), nil
				}
				if exists {
					matchedAsset = key
					break
				}
			}
			if matchedAsset == "" {
				return actool.Errorf("The target task has not registered this Finding's asset; cannot append"), nil
			}

			marker := findingHandoffMarker(findingID)
			reviveTarget := func() (bool, error) {
				lifecycle := target.lifecycleSnapshot()
				if lifecycle.Status == "running" && !lifecycle.Paused && !lifecycle.Queued {
					return false, nil
				}
				_, err := s.admitTask(target, "resume")
				return err == nil, err
			}
			var existingHintID int64
			err = s.m.pg.QueryRow(`SELECT id FROM exploration_nodes
				WHERE exploration_id=$1 AND kind='hint' AND payload->>'text' LIKE $2
				ORDER BY id LIMIT 1`, target.ExpID, marker+"%").Scan(&existingHintID)
			if err == nil {
				revived, reviveErr := reviveTarget()
				if reviveErr != nil {
					return actool.Errorf(fmt.Sprintf("Finding was appended, but task recovery failed: %v", reviveErr)), nil
				}
				return jsonResult(map[string]any{
					"status": "already_appended", "task_id": taskID,
					"finding_id": findingID, "hint_id": existingHintID, "revived": revived,
				})
			}
			if err != sql.ErrNoRows {
				return actool.Errorf("Failed to check Finding append records: " + err.Error()), nil
			}

			// A terminal task with no open goal would swallow a plain hint in the
			// goalless engine branch. Add an idempotent follow-up goal before revive.
			var goalID int64
			if isTerminalStatus(target.lifecycleSnapshot().Status) {
				openGoal, goalErr := target.Store.HasOpenGoal()
				if goalErr != nil {
					return actool.Errorf(fmt.Sprintf("Failed to check target task: %v", goalErr)), nil
				}
				if !openGoal {
					goalText := fmt.Sprintf("%s Evaluate and handle new Finding #%d for the same asset; include it in the internal penetration testing plan.", marker, findingID)
					var goalState string
					goalErr = s.m.pg.QueryRow(`SELECT id,state FROM exploration_nodes
						WHERE exploration_id=$1 AND kind='goal' AND payload->>'text' LIKE $2
						ORDER BY id DESC LIMIT 1`, target.ExpID, marker+"%").Scan(&goalID, &goalState)
					switch goalErr {
					case nil:
						if goalState != "open" {
							if _, goalErr = s.m.pg.Exec(`UPDATE exploration_nodes SET state='open' WHERE id=$1`, goalID); goalErr != nil {
								return actool.Errorf(fmt.Sprintf("Failed to reopen Finding goal: %v", goalErr)), nil
							}
							target.NotifyGoal([]string{goalText})
						}
					case sql.ErrNoRows:
						goalID, goalErr = target.Store.AddGoal(map[string]any{"text": goalText, "vulnclass": finding.VulnClass}, "vuln_dispatcher")
						if goalErr != nil {
							return actool.Errorf(fmt.Sprintf("Failed to add Finding goal: %v", goalErr)), nil
						}
						if of, _ := target.Store.OriginFactID(); of > 0 {
							_ = target.Store.Link(of, db.RelSpawns, goalID)
						}
						target.NotifyGoal([]string{goalText})
					default:
						return actool.Errorf(fmt.Sprintf("Failed to read Finding goal: %v", goalErr)), nil
					}
				}
			}
			revived, reviveErr := reviveTarget()
			if reviveErr != nil {
				return actool.Errorf(fmt.Sprintf("Failed to revive internal task: %v", reviveErr)), nil
			}

			text := findingHandoffText(finding)
			payload, _ := json.Marshal(map[string]any{
				"task_id": taskID, "text": text, "asset_ids": finding.AssetIDs,
			})
			res, err := s.delegateToTask(ctx, payload, (*agent.ToolSet).AddHintTool)
			if err != nil || res.IsError {
				return res, err
			}
			var hintID int64
			_, _ = fmt.Sscanf(res.Flatten(), "hint added: %d", &hintID)
			auditID, auditErr := target.Store.AppendActivity(db.Activity{
				NodeID: &hintID, Worker: "vuln_dispatcher", Kind: "finding_handoff",
				Summary: fmt.Sprintf("Finding #%d appended to the internal subtask for the same asset", findingID),
				Detail:  fmt.Sprintf("task_id=%s finding_id=%d hint_id=%d goal_id=%d asset=%s revived=%t", taskID, findingID, hintID, goalID, matchedAsset, revived),
			})
			if auditErr != nil {
				log.Printf("[dispatcher] failed to record Finding append audit task=%s finding=%d: %v", taskID, findingID, auditErr)
			}
			return jsonResult(map[string]any{
				"status": "appended", "task_id": taskID, "finding_id": findingID,
				"hint_id": hintID, "audit_id": auditID, "goal_id": goalID,
				"asset_key": matchedAsset, "revived": revived,
			})
		})
}
