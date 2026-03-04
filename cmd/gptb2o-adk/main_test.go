package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
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

func TestBuildUserMessage_TextOnly(t *testing.T) {
	msg, err := buildUserMessage("hello", "", "")
	require.NoError(t, err)
	require.Equal(t, schema.User, msg.Role)
	require.Equal(t, "hello", msg.Content)
	require.Empty(t, msg.UserInputMultiContent)
}

func TestBuildUserMessage_TextAndImageURL(t *testing.T) {
	msg, err := buildUserMessage("describe", "https://example.com/a.png", "high")
	require.NoError(t, err)
	require.Equal(t, schema.User, msg.Role)
	require.Empty(t, msg.Content)
	require.Len(t, msg.UserInputMultiContent, 2)

	textPart := msg.UserInputMultiContent[0]
	require.Equal(t, schema.ChatMessagePartTypeText, textPart.Type)
	require.Equal(t, "describe", textPart.Text)

	imagePart := msg.UserInputMultiContent[1]
	require.Equal(t, schema.ChatMessagePartTypeImageURL, imagePart.Type)
	require.NotNil(t, imagePart.Image)
	require.NotNil(t, imagePart.Image.URL)
	require.Equal(t, "https://example.com/a.png", *imagePart.Image.URL)
	require.Equal(t, schema.ImageURLDetailHigh, imagePart.Image.Detail)
}

func TestBuildUserMessage_LocalImageFile(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "tiny.png")
	pngBase64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO5m9R0AAAAASUVORK5CYII="
	data, err := base64.StdEncoding.DecodeString(pngBase64)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(imagePath, data, 0o600))

	msg, err := buildUserMessage("", imagePath, "low")
	require.NoError(t, err)
	require.Equal(t, schema.User, msg.Role)
	require.Len(t, msg.UserInputMultiContent, 1)

	imagePart := msg.UserInputMultiContent[0]
	require.Equal(t, schema.ChatMessagePartTypeImageURL, imagePart.Type)
	require.NotNil(t, imagePart.Image)
	require.Nil(t, imagePart.Image.URL)
	require.NotNil(t, imagePart.Image.Base64Data)
	require.NotEmpty(t, *imagePart.Image.Base64Data)
	require.Equal(t, "image/png", imagePart.Image.MIMEType)
	require.Equal(t, schema.ImageURLDetailLow, imagePart.Image.Detail)
}

func TestBuildUserMessage_InvalidImageDetail(t *testing.T) {
	_, err := buildUserMessage("hello", "https://example.com/a.png", "ultra")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid image-detail")
}
