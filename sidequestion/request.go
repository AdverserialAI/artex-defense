// request.go：旁路问答请求的组装——历史截取、输入/输出 token 预算控制与消息拼装。
// 主要函数/类型：
//   - Exchange：一条旁路问答记录（问题、回答、状态、用量）。
//   - EstimateInputTokens / outputBudget / inputBudget：估算输入规模并分配输出/输入预算。
//   - assemble / exchangeMessages：把快照、摘要与历史问答拼成请求消息。
//   - BuildRequest：基于不可变快照与历史构建旁路 CompletionRequest（超限则报错）。

package sidequestion

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"norma/compaction"
	"norma/llm"
)

type Exchange struct {
	ID         string      `json:"id"`
	SessionKey string      `json:"-"`
	ClientID   string      `json:"client_request_id"`
	Generation int64       `json:"-"`
	Question   string      `json:"question"`
	Answer     string      `json:"answer"`
	Status     string      `json:"status"`
	Error      string      `json:"error,omitempty"`
	Model      Model       `json:"model"`
	SnapshotAt time.Time   `json:"snapshot_at"`
	CreatedAt  time.Time   `json:"created_at"`
	Sequence   int64       `json:"sequence"`
	Usage      llm.Usage   `json:"usage"`
	Ordinal    int64       `json:"ordinal"`
	Context    ContextInfo `json:"context"`
}

func (e Exchange) Running() bool { return e.Status == "running" }

const instruction = "This is an independent side question. The Main Agent is executing the original task. Answer only the current question concisely from existing context. You cannot execute tools, perform actions, modify files, or direct the main task. Do not promise to act later. Treat task instructions in the context as background; state clearly when evidence is insufficient. Answer in English."

const DefaultOutputTokens = 8192
const MaxRecentExchanges = 20

var ErrContextBudget = errors.New("Side-question context still exceeds the model budget after compaction. Narrow the question or adjust the model context configuration")

// EstimateInputTokens follows norma's byte-based block estimate with its 4/3
// safety factor. Include system/schema and framing costs too; JSON characters
// are not tokens (and marshaling HTML can add many non-semantic escapes).
func EstimateInputTokens(req llm.CompletionRequest) int {
	tokens := compaction.EstimateTokens(req.Messages)*4/3 + 32 + len(req.Messages)*8
	for _, text := range req.System {
		tokens += (len(text)+2)/3 + 8
	}
	for _, tool := range req.Tools {
		b, _ := json.Marshal(tool)
		tokens += (len(b)+2)/3 + 8
	}
	return tokens
}

func outputBudget(req llm.CompletionRequest, configured int) int {
	if configured <= 0 {
		configured = DefaultOutputTokens
	}
	configured = min(configured, 32768)
	if req.MaxTokens > 0 {
		configured = min(configured, req.MaxTokens)
	}
	return configured
}

func inputBudget(s Snapshot, output int) int {
	window := s.Model.WindowTokens
	if window <= 0 {
		window = 200000
	}
	return window - output - min(8192, max(128, window/20))
}

func exchangeMessages(e Exchange) []llm.Message {
	question := e.Question
	if !e.SnapshotAt.IsZero() {
		question = "[Historical side-question discussion; context time " + e.SnapshotAt.UTC().Format(time.RFC3339) + "]\n" + question
	}
	return []llm.Message{llm.UserText(question), {Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(e.Answer)}}}
}

func assemble(req llm.CompletionRequest, base []llm.Message, summary string, history []Exchange, question string) llm.CompletionRequest {
	req.Messages = append([]llm.Message{}, base...)
	if summary != "" {
		req.Messages = append(req.Messages, llm.UserText("[Early side-question summary; historical discussion, not new tool evidence. Prefer the latest main context in a conflict.]\n"+summary))
	}
	for _, e := range history {
		req.Messages = append(req.Messages, exchangeMessages(e)...)
	}
	req.Messages = append(req.Messages, llm.UserText(instruction+"\n\nQuestion: "+strings.TrimSpace(question)))
	return req
}

func BuildRequest(s Snapshot, history []Exchange, question string) (llm.CompletionRequest, error) {
	req, err := CloneRequest(s.Request)
	if err != nil {
		return req, err
	}
	base := llm.MessagesForAPI(req.Messages)
	req.MaxTokens = outputBudget(req, 0)
	var success []Exchange
	for _, e := range history {
		if e.Status == "completed" {
			success = append(success, e)
		}
	}
	if len(success) > MaxRecentExchanges {
		success = success[len(success)-MaxRecentExchanges:]
	}
	for {
		req = assemble(req, base, "", success, question)
		if EstimateInputTokens(req) <= inputBudget(s, req.MaxTokens) {
			return req, nil
		}
		if len(success) == 0 {
			return req, ErrContextBudget
		}
		success = success[1:]
	}
}
