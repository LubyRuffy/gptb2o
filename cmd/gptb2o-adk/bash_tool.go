package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const (
	bashToolName = "bash"
	bashToolDesc = "Execute a bash shell command and return the combined stdout/stderr output. " +
		"Use this to run system commands, inspect files, check environment, or perform any shell operation."
)

type bashInput struct {
	Command string `json:"command"`
}

// bashTool implements tool.InvokableTool to execute arbitrary bash commands.
// Note: intended for development/testing only; no sandboxing is applied.
type bashTool struct{}

func newBashTool() tool.InvokableTool {
	return &bashTool{}
}

// Info returns the tool metadata used by the chat model for intent recognition.
func (t *bashTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: bashToolName,
		Desc: bashToolDesc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"command": {
				Type:     schema.String,
				Desc:     "The bash command to execute.",
				Required: true,
			},
		}),
	}, nil
}

// InvokableRun executes the given bash command and returns stdout+stderr combined.
// Non-zero exit codes are reported in the output rather than returned as errors,
// so the model can reason about the failure.
func (t *bashTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var input bashInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", fmt.Errorf("bash tool: invalid arguments: %w", err)
	}
	cmd := strings.TrimSpace(input.Command)
	if cmd == "" {
		return "", fmt.Errorf("bash tool: command must not be empty")
	}

	slog.InfoContext(ctx, "bash tool executing", "command", cmd)

	var buf bytes.Buffer
	c := exec.CommandContext(ctx, "bash", "-c", cmd)
	c.Stdout = &buf
	c.Stderr = &buf

	if err := c.Run(); err != nil {
		output := strings.TrimRight(buf.String(), "\n")
		slog.WarnContext(ctx, "bash tool command failed", "command", cmd, "err", err)
		return fmt.Sprintf("exit error: %v\n%s", err, output), nil
	}
	return buf.String(), nil
}
