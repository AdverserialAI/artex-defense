// dispatch_assets.go：内网渗透派发目标的资产解析、历史回填与子任务查重工具。
// 主要函数/类型：
//   - findingDispatchAssetKeys：按 Finding 实际关联主机解析派发资产键，多主机时必须指定 target_asset。
//   - resolveSpawnTaskName：单资产内网任务按主机生成任务名。
//   - backfillTaskDispatchTargets：为历史内网任务回填派发资产目标。
//   - toolCheckInternalAssetTask：供 agent 调用，检查 Finding 是否已创建内网子任务。

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	"artex/db"
	actool "norma/tool"
)

// resolveSpawnTaskName gives every internal child a stable, asset-based display
// name. The asset key has already been validated by findingDispatchAssetKeys.
func resolveSpawnTaskName(scenarioNKey, raw string, assetKeys []string) string {
	name := strings.TrimSpace(raw)
	if scenarioNKey != "内网渗透" || len(assetKeys) != 1 {
		return name
	}
	host := strings.TrimPrefix(strings.TrimPrefix(assetKeys[0], "ip:"), "domain:")
	if host == "" {
		return name
	}
	return "Internal Penetration Testing · " + host
}

// findingDispatchAssetKeys resolves the actual host linked to the standalone
// Finding, not every asset inherited from the parent task. Multiple services or
// endpoints on one host collapse to one key; multiple hosts require an explicit
// target_asset selected from those linked to the Finding.
func (s *Server) findingDispatchAssetKeys(findingID int64, targetAsset string) ([]string, error) {
	if findingID <= 0 {
		return nil, fmt.Errorf("Internal-network dispatch requires finding_id for deduplication by actual asset")
	}
	finding, err := s.m.PG().GetFinding(findingID)
	if err != nil {
		return nil, fmt.Errorf("Failed to read Finding #%d: %w", findingID, err)
	}
	if finding == nil {
		return nil, fmt.Errorf("Finding #%d does not exist", findingID)
	}
	assets, err := s.m.PG().Assets().GetByIDs(finding.AssetIDs)
	if err != nil {
		return nil, fmt.Errorf("Failed to read assets linked to Finding #%d: %w", findingID, err)
	}
	keys := db.CanonicalAssetKeys(assets)
	if len(keys) == 0 {
		return nil, fmt.Errorf("Finding #%d has no IP/domain assets that can be normalized; cannot dispatch by asset", findingID)
	}
	targetAsset = strings.TrimSpace(targetAsset)
	if targetAsset == "" {
		if len(keys) > 1 {
			return nil, fmt.Errorf("Finding #%d is linked to multiple hosts %v; select the primary target using target_asset", findingID, keys)
		}
		return keys, nil
	}
	selected := db.CanonicalAssetKey(&db.Asset{IP: targetAsset})
	if selected == "" {
		return nil, fmt.Errorf("target_asset %q is not a valid IP/domain", targetAsset)
	}
	for _, key := range keys {
		if key == selected {
			return []string{key}, nil
		}
	}
	return nil, fmt.Errorf("target_asset %q is not among Finding #%d's linked assets %v", targetAsset, findingID, keys)
}

var dispatchFindingIDPattern = regexp.MustCompile(`(?i)finding(?:_id)?\s*(?:#|=)\s*([0-9]+)`)

// backfillTaskDispatchTargets registers legacy internal tasks whose seven-part
// description contains finding #N or finding_id=N. Tasks are processed oldest-first, so a
// historical duplicate keeps the earliest task as the canonical dispatch
// target. Ambiguous multi-host tasks are skipped rather than guessed.
func (s *Server) backfillTaskDispatchTargets() {
	if s == nil || s.m == nil {
		return
	}
	rows, err := s.m.PG().Query(`SELECT t.id,t.description,t.goal FROM tasks t
		JOIN task_categories c ON c.id=t.category_id
		WHERE c.nkey='内网渗透'
		AND NOT EXISTS (SELECT 1 FROM task_dispatch_targets d WHERE d.task_id=t.id)
		ORDER BY t.id`)
	if err != nil {
		log.Printf("[dispatch] historical asset-target scan failed: %v", err)
		return
	}
	defer rows.Close()
	type candidate struct {
		id   int64
		text string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		var description, goal string
		if err := rows.Scan(&c.id, &description, &goal); err == nil {
			c.text = description + "\n" + goal
			candidates = append(candidates, c)
		}
	}
	for _, c := range candidates {
		matches := dispatchFindingIDPattern.FindAllStringSubmatch(c.text, -1)
		keys := map[string]bool{}
		valid := len(matches) > 0
		for _, match := range matches {
			findingID, _ := strconv.ParseInt(match[1], 10, 64)
			found, err := s.findingDispatchAssetKeys(findingID, "")
			if err != nil {
				valid = false
				break
			}
			for _, key := range found {
				keys[key] = true
			}
		}
		if !valid || len(keys) != 1 {
			continue
		}
		for key := range keys {
			var findingID int64
			findingID, _ = strconv.ParseInt(matches[0][1], 10, 64)
			if _, err := s.m.PG().Exec(`INSERT INTO task_dispatch_targets
				(task_id,finding_id,asset_key) VALUES ($1,$2,$3) ON CONFLICT(asset_key) DO NOTHING`, c.id, findingID, key); err != nil {
				log.Printf("[dispatch] failed to backfill asset targets for historical task #%d: %v", c.id, err)
			}
		}
	}
}

func (s *Server) toolCheckInternalAssetTask() actool.CoreTool {
	return roTool("check_internal_asset_task",
		"Check whether an internal penetration-testing subtask already exists for the IP or domain associated with a Finding. Different ports on the same host share one asset key. Must be called before spawn_task.",
		objSchema(map[string]any{
			"finding_id":   map[string]any{"type": "integer", "description": "Global ID of the Finding that triggered dispatch"},
			"target_asset": strParam("Optional: when the Finding links to multiple hosts, select the primary target IP/domain; it must belong to this Finding"),
		}, "finding_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				FindingID   int64  `json:"finding_id"`
				TargetAsset string `json:"target_asset"`
			}
			_ = json.Unmarshal(in, &a)
			keys, err := s.findingDispatchAssetKeys(a.FindingID, a.TargetAsset)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			targets, err := s.m.PG().ListTaskDispatchTargets(keys)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("Failed to query internal asset dispatch records: %v", err)), nil
			}
			return jsonResult(map[string]any{
				"finding_id": a.FindingID,
				"asset_keys": keys,
				"exists":     len(targets) > 0,
				"tasks":      targets,
			})
		})
}
