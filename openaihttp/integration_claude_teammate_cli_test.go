package openaihttp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LubyRuffy/gptb2o"
	"github.com/LubyRuffy/gptb2o/openaihttp"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 真实 Claude Code CLI 集成测试：
// 1) 启动本地 gptb2o 服务（/v1/messages）
// 2) 启动 fake backend，首轮返回 Task tool_use，次轮校验 function_call_output
// 3) 调用 claude CLI 验证 teammate 调用链路
//
// 默认跳过，避免在无本地 claude 环境时影响常规测试。
//
// 运行示例：
//
//	GPTB2O_RUN_CLAUDE_IT=1 go test ./openaihttp -run TeammateCLI -v
func TestIntegration_ClaudeMessages_TeammateCLI_TaskRoundTrip(t *testing.T) {
	if strings.TrimSpace(os.Getenv("GPTB2O_RUN_CLAUDE_IT")) == "" {
		t.Skip("set GPTB2O_RUN_CLAUDE_IT=1 to run Claude CLI teammate integration test")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude command not found in PATH")
	}

	gin.SetMode(gin.TestMode)

	var (
		reqCount          int32
		sawTaskSchema     atomic.Bool
		sawTaskToolResult atomic.Bool
		stateMu           sync.Mutex
		taskSubagentType  = "code-simplifier:code-simplifier"
		lastTurnPayload   atomic.Value
	)

	backendSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		turn := atomic.AddInt32(&reqCount, 1)

		var payload struct {
			Input []struct {
				Type      string `json:"type"`
				CallID    string `json:"call_id,omitempty"`
				Name      string `json:"name,omitempty"`
				Output    string `json:"output,omitempty"`
				Arguments string `json:"arguments,omitempty"`
			} `json:"input"`
			Tools []struct {
				Type       string         `json:"type"`
				Name       string         `json:"name,omitempty"`
				Parameters map[string]any `json:"parameters,omitempty"`
			} `json:"tools"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		lastTurnPayload.Store(payload)

		w.Header().Set("Content-Type", "text/event-stream")

		if turn == 1 {
			if hasTaskCoreSchema(payload.Tools) {
				sawTaskSchema.Store(true)
			}
			if selected := pickTaskSubagentType(payload.Tools); selected != "" {
				stateMu.Lock()
				taskSubagentType = selected
				stateMu.Unlock()
			}

			stateMu.Lock()
			selectedSubagent := taskSubagentType
			stateMu.Unlock()
			args := map[string]any{
				"description":   "Run teammate integration smoke test",
				"prompt":        "请只回复 TEAMMATE_CLI_OK，不做文件修改。",
				"subagent_type": selectedSubagent,
				"max_turns":     1,
			}
			argsJSON, _ := json.Marshal(args)

			writeBackendSSE(t, w, map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id": "resp_teammate_turn_1",
					"output": []map[string]any{
						{
							"id":        "fc_teammate_1",
							"type":      "function_call",
							"call_id":   "call_teammate_1",
							"name":      "Task",
							"arguments": string(argsJSON),
							"status":    "completed",
						},
					},
				},
			})
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}

		for _, item := range payload.Input {
			if item.Type != "function_call_output" || strings.TrimSpace(item.CallID) != "call_teammate_1" {
				continue
			}
			if strings.TrimSpace(item.Output) != "" {
				sawTaskToolResult.Store(true)
				break
			}
		}

		writeBackendSSE(t, w, map[string]any{
			"type":  "response.output_text.delta",
			"delta": "TEAMMATE_CLI_OK",
		})
		writeBackendSSE(t, w, map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"id": "resp_teammate_turn_2",
			},
		})
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(backendSrv.Close)

	gateway := gin.New()
	require.NoError(t, openaihttp.RegisterGinRoutes(gateway, openaihttp.Config{
		BasePath:   "/v1",
		BackendURL: backendSrv.URL,
		HTTPClient: backendSrv.Client(),
		Originator: "integration-test",
		AuthProvider: func(ctx context.Context) (string, string, error) {
			return "token", "", nil
		},
	}))
	gatewaySrv := httptest.NewServer(gateway)
	t.Cleanup(gatewaySrv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	cmd := exec.CommandContext(
		ctx,
		"claude",
		"--setting-sources", "project,local",
		"--model", gptb2o.ModelNamespace+"gpt-5.3-codex",
		"--permission-mode", "bypassPermissions",
		"--print",
		"请执行 teammate 集成测试并返回结果。",
	)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"ANTHROPIC_BASE_URL="+gatewaySrv.URL,
		"ANTHROPIC_API_KEY=dummy",
	)

	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf(
			"claude command timed out: %v, output=%s, backend_req_count=%d, saw_task_schema=%v, saw_task_tool_result=%v, last_turn_payload=%#v",
			ctx.Err(),
			string(out),
			atomic.LoadInt32(&reqCount),
			sawTaskSchema.Load(),
			sawTaskToolResult.Load(),
			lastTurnPayload.Load(),
		)
	}
	require.NoError(t, err, "claude command failed, output=%s", string(out))

	output := string(out)
	require.NotContains(t, output, "Invalid tool parameters", "output=%s", output)
	require.Contains(t, output, "TEAMMATE_CLI_OK", "output=%s", output)
	require.True(t, sawTaskSchema.Load(), "first turn did not expose Task schema with required fields")
	require.True(t, sawTaskToolResult.Load(), "second turn did not receive function_call_output for teammate Task")
	require.GreaterOrEqual(t, atomic.LoadInt32(&reqCount), int32(2), "backend did not observe second turn")
}

func writeBackendSSE(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
}

func hasTaskCoreSchema(tools []struct {
	Type       string         `json:"type"`
	Name       string         `json:"name,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
}) bool {
	for _, tool := range tools {
		if !strings.EqualFold(strings.TrimSpace(tool.Type), "function") {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(tool.Name), "Task") {
			continue
		}
		requiredRaw, ok := tool.Parameters["required"]
		if !ok {
			return false
		}
		requiredItems, ok := requiredRaw.([]any)
		if !ok {
			return false
		}
		required := make(map[string]struct{}, len(requiredItems))
		for _, v := range requiredItems {
			s, _ := v.(string)
			s = strings.TrimSpace(s)
			if s != "" {
				required[s] = struct{}{}
			}
		}
		_, hasDescription := required["description"]
		_, hasPrompt := required["prompt"]
		_, hasSubagentType := required["subagent_type"]
		return hasDescription && hasPrompt && hasSubagentType
	}
	return false
}

func pickTaskSubagentType(tools []struct {
	Type       string         `json:"type"`
	Name       string         `json:"name,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
}) string {
	for _, tool := range tools {
		if !strings.EqualFold(strings.TrimSpace(tool.Type), "function") {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(tool.Name), "Task") {
			continue
		}
		propsRaw, ok := tool.Parameters["properties"]
		if !ok {
			continue
		}
		props, ok := propsRaw.(map[string]any)
		if !ok {
			continue
		}
		subagentRaw, ok := props["subagent_type"]
		if !ok {
			continue
		}
		subagentObj, ok := subagentRaw.(map[string]any)
		if !ok {
			continue
		}
		enumRaw, ok := subagentObj["enum"]
		if !ok {
			continue
		}
		enumItems, ok := enumRaw.([]any)
		if !ok {
			continue
		}
		for _, v := range enumItems {
			s, _ := v.(string)
			s = strings.TrimSpace(s)
			if s != "" {
				return s
			}
		}
	}
	return ""
}
