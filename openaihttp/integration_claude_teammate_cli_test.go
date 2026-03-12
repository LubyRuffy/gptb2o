package openaihttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sort"
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
// 2) 启动 fake backend，首轮返回 Agent/Task tool_use，次轮校验 function_call_output
// 3) 调用 claude CLI 验证 teammate 调用链路
//
// 默认跳过，避免在无本地 claude 环境时影响常规测试。
//
// 运行示例：
//
//	GPTB2O_RUN_CLAUDE_IT=1 go test ./openaihttp -run TeammateCLI -v
func TestIntegration_ClaudeMessages_TeammateCLI_TaskRoundTrip(t *testing.T) {
	assertClaudeMessagesTeammateCLIRoundTrip(t, "Task")
}

func TestIntegration_ClaudeMessages_TeammateCLI_AgentRoundTrip(t *testing.T) {
	assertClaudeMessagesTeammateCLIRoundTrip(t, "Agent")
}

func assertClaudeMessagesTeammateCLIRoundTrip(t *testing.T, preferredBootstrapTool string) {
	if strings.TrimSpace(os.Getenv("GPTB2O_RUN_CLAUDE_IT")) == "" {
		t.Skip("set GPTB2O_RUN_CLAUDE_IT=1 to run Claude CLI teammate integration test")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude command not found in PATH")
	}

	gin.SetMode(gin.TestMode)

	var (
		reqCount           int32
		sawBootstrapSchema atomic.Bool
		sawBootstrapResult atomic.Bool
		stateMu            sync.Mutex
		bootstrapToolName  = preferredBootstrapTool
		taskSubagentType   = "code-simplifier:code-simplifier"
		lastTurnPayload    atomic.Value
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
			if toolName, ok := findClaudeBootstrapTool(payload.Tools); ok {
				sawBootstrapSchema.Store(true)
				stateMu.Lock()
				bootstrapToolName = toolName
				stateMu.Unlock()
			}
			if strings.EqualFold(strings.TrimSpace(preferredBootstrapTool), "Agent") {
				stateMu.Lock()
				bootstrapToolName = "Agent"
				stateMu.Unlock()
			}
			if selected := pickTaskSubagentType(payload.Tools); selected != "" {
				stateMu.Lock()
				taskSubagentType = selected
				stateMu.Unlock()
			}

			stateMu.Lock()
			selectedTool := bootstrapToolName
			selectedSubagent := taskSubagentType
			stateMu.Unlock()
			args := buildBootstrapToolArgs(payload.Tools, selectedTool, selectedSubagent)
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
							"name":      selectedTool,
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
				sawBootstrapResult.Store(true)
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

	type gatewayReq struct {
		Method string
		Path   string
		Body   string
		JSON   map[string]any
	}
	var (
		gatewayReqMu sync.Mutex
		gatewayReqs  []gatewayReq
	)

	gateway := gin.New()
	gateway.Use(func(c *gin.Context) {
		req := c.Request
		entry := gatewayReq{
			Method: req.Method,
			Path:   req.URL.Path,
		}
		if req.Body != nil {
			bodyBytes, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
			_ = req.Body.Close()
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			entry.Body = string(bodyBytes)
			if len(bodyBytes) > 0 && strings.Contains(strings.ToLower(req.Header.Get("Content-Type")), "application/json") {
				var decoded map[string]any
				if err := json.Unmarshal(bodyBytes, &decoded); err == nil {
					entry.JSON = decoded
				}
			}
		}
		gatewayReqMu.Lock()
		gatewayReqs = append(gatewayReqs, entry)
		gatewayReqMu.Unlock()
		c.Next()
	})
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

	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
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
			"claude command timed out: %v, output=%s, backend_req_count=%d, saw_bootstrap_schema=%v, saw_bootstrap_tool_result=%v, last_turn_payload=%#v",
			ctx.Err(),
			string(out),
			atomic.LoadInt32(&reqCount),
			sawBootstrapSchema.Load(),
			sawBootstrapResult.Load(),
			lastTurnPayload.Load(),
		)
	}
	require.NoError(t, err, "claude command failed, output=%s", string(out))

	output := string(out)
	require.NotContains(t, output, "Invalid tool parameters", "output=%s", output)
	require.Contains(t, output, "TEAMMATE_CLI_OK", "output=%s", output)
	require.True(t, sawBootstrapSchema.Load(), "first turn did not expose Agent/Task schema with required fields")
	require.True(t, sawBootstrapResult.Load(), "second turn did not receive function_call_output for teammate Agent/Task")
	require.GreaterOrEqual(t, atomic.LoadInt32(&reqCount), int32(2), "backend did not observe second turn")

	gatewayReqMu.Lock()
	defer gatewayReqMu.Unlock()
	require.NotEmpty(t, gatewayReqs, "gateway should capture requests from claude CLI")
	pathCount := make(map[string]int)
	for _, r := range gatewayReqs {
		pathCount[r.Path]++
	}
	paths := make([]string, 0, len(pathCount))
	for p := range pathCount {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		t.Logf("claude gateway observed: %s x%d", p, pathCount[p])
	}

	var lastMessagesJSON map[string]any
	for i := len(gatewayReqs) - 1; i >= 0; i-- {
		if gatewayReqs[i].Path != "/v1/messages" || gatewayReqs[i].JSON == nil {
			continue
		}
		lastMessagesJSON = gatewayReqs[i].JSON
		break
	}
	require.NotNil(t, lastMessagesJSON, "missing parsed /v1/messages request JSON from claude CLI")
	keys := make([]string, 0, len(lastMessagesJSON))
	for k := range lastMessagesJSON {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	t.Logf("claude /v1/messages keys: %v", keys)
}

func writeBackendSSE(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
}

func findClaudeBootstrapTool(tools []struct {
	Type       string         `json:"type"`
	Name       string         `json:"name,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
}) (string, bool) {
	for _, tool := range tools {
		if !strings.EqualFold(strings.TrimSpace(tool.Type), "function") {
			continue
		}
		name := strings.TrimSpace(tool.Name)
		if !isClaudeBootstrapToolName(name) {
			continue
		}
		if hasBootstrapCoreSchema(tool) {
			return name, true
		}
	}
	return "", false
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
		if !isClaudeBootstrapToolName(tool.Name) {
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
		if enumRaw, ok := subagentObj["enum"]; ok {
			if enumItems, ok := enumRaw.([]any); ok {
				for _, v := range enumItems {
					s, _ := v.(string)
					s = strings.TrimSpace(s)
					if s != "" {
						return s
					}
				}
			}
		}
		return "general-purpose"
	}
	return ""
}

func hasBootstrapCoreSchema(tool struct {
	Type       string         `json:"type"`
	Name       string         `json:"name,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
}) bool {
	required := requiredFieldSet(tool.Parameters)
	if _, ok := required["description"]; !ok {
		return false
	}
	if _, ok := required["prompt"]; !ok {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(tool.Name), "Task") {
		_, ok := required["subagent_type"]
		return ok
	}

	props := toolProperties(tool.Parameters)
	_, ok := props["subagent_type"]
	return ok
}

func buildBootstrapToolArgs(tools []struct {
	Type       string         `json:"type"`
	Name       string         `json:"name,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
}, toolName string, subagentType string) map[string]any {
	args := map[string]any{
		"description": "Run teammate integration smoke test",
		"prompt":      "请只回复 TEAMMATE_CLI_OK，不做文件修改。",
	}
	for _, tool := range tools {
		if !strings.EqualFold(strings.TrimSpace(tool.Type), "function") {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(tool.Name), strings.TrimSpace(toolName)) {
			continue
		}
		props := toolProperties(tool.Parameters)
		if _, ok := props["subagent_type"]; ok && strings.TrimSpace(subagentType) != "" {
			args["subagent_type"] = subagentType
		}
		return args
	}
	args["subagent_type"] = subagentType
	return args
}

func requiredFieldSet(params map[string]any) map[string]struct{} {
	required := make(map[string]struct{})
	requiredRaw, ok := params["required"]
	if !ok {
		return required
	}
	requiredItems, ok := requiredRaw.([]any)
	if !ok {
		return required
	}
	for _, v := range requiredItems {
		s, _ := v.(string)
		s = strings.TrimSpace(s)
		if s != "" {
			required[s] = struct{}{}
		}
	}
	return required
}

func toolProperties(params map[string]any) map[string]any {
	propsRaw, ok := params["properties"]
	if !ok {
		return nil
	}
	props, ok := propsRaw.(map[string]any)
	if !ok {
		return nil
	}
	return props
}

func isClaudeBootstrapToolName(name string) bool {
	name = strings.TrimSpace(name)
	return strings.EqualFold(name, "Task") || strings.EqualFold(name, "Agent")
}
