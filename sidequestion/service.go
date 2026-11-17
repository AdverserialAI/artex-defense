// service.go：旁路问答服务——调用模型生成答案（支持流式与非流式），并回调更新进度。
// 主要函数/类型：
//   - SideQuestionService：旁路问答服务，包装一个 LLM Provider。
//   - Answer：执行一次旁路问答，流式拼接文本、检测工具调用意图并累计用量。

package sidequestion

import (
	"context"
	"errors"
	"strings"

	"norma/llm"
)

// SideQuestionService has no harness, tool executor, transcript writer or model
// failover chain. Answer is one completion; Respond adds bounded preparation
// and at most one context-overflow recovery around that completion.
type SideQuestionService struct{ Provider llm.Provider }

type Answer struct {
	Text    string
	Usage   llm.Usage
	ToolUse bool
}

func (s SideQuestionService) Answer(ctx context.Context, req llm.CompletionRequest, streaming bool, update func(Answer)) (out Answer, err error) {
	if streaming {
		complete := false
		for ev, streamErr := range s.Provider.Stream(ctx, req) {
			if streamErr != nil {
				err = streamErr
				break
			}
			switch ev.Type {
			case llm.SETextDelta:
				out.Text += ev.Text
			case llm.SEToolUseStart:
				out.ToolUse = true
			case llm.SEMessageStart, llm.SEMessageDelta:
				out.Usage.Add(ev.Usage)
			case llm.SEMessageStop:
				complete = true
			}
			if update != nil {
				update(out)
			}
			if ctx.Err() != nil {
				err = ctx.Err()
				break
			}
		}
		if err == nil && !complete {
			err = errors.New("Model response was interrupted. Ask again")
		}
	} else {
		var msg llm.Message
		msg, _, out.Usage, err = s.Provider.Complete(ctx, req)
		out.Text, out.ToolUse = msg.Text(), len(msg.ToolUses()) > 0
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil && strings.TrimSpace(out.Text) == "" {
		if out.ToolUse {
			out.Text = "Side questions cannot execute tools. Make action requests in the main conversation."
		} else {
			err = errors.New("Model returned no answer")
		}
	}
	return out, err
}
