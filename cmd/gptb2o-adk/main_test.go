package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultAgentNameNotEmpty(t *testing.T) {
	// 防止回归：eino/adk 的 ChatModelAgentConfig 要求 Name 必填。
	if defaultAgentName == "" {
		t.Fatalf("defaultAgentName should not be empty")
	}
}

func TestBashToolInfo(t *testing.T) {
	bt := newBashTool()
	info, err := bt.Info(context.Background())
	require.NoError(t, err)
	require.Equal(t, bashToolName, info.Name)
	require.NotEmpty(t, info.Desc)
	require.NotNil(t, info.ParamsOneOf)
}

func TestBashToolRun_Echo(t *testing.T) {
	bt := newBashTool()
	out, err := bt.InvokableRun(context.Background(), `{"command":"echo hello"}`)
	require.NoError(t, err)
	require.Contains(t, out, "hello")
}

func TestBashToolRun_CombinesStderr(t *testing.T) {
	bt := newBashTool()
	// bash -c "echo err >&2; echo out" 应同时返回 stdout 和 stderr
	out, err := bt.InvokableRun(context.Background(), `{"command":"echo err >&2; echo out"}`)
	require.NoError(t, err)
	require.Contains(t, out, "out")
	require.Contains(t, out, "err")
}

func TestBashToolRun_NonZeroExitReportedInOutput(t *testing.T) {
	bt := newBashTool()
	out, err := bt.InvokableRun(context.Background(), `{"command":"exit 1"}`)
	// 非零退出码应体现在输出中，而不是作为 error 返回
	require.NoError(t, err)
	require.True(t, strings.Contains(out, "exit") || strings.Contains(out, "1"),
		"expected exit error info in output, got: %q", out)
}

func TestBashToolRun_InvalidJSON(t *testing.T) {
	bt := newBashTool()
	_, err := bt.InvokableRun(context.Background(), `not-json`)
	require.Error(t, err)
}

func TestBashToolRun_EmptyCommand(t *testing.T) {
	bt := newBashTool()
	_, err := bt.InvokableRun(context.Background(), `{"command":""}`)
	require.Error(t, err)
}
