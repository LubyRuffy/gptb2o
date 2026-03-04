package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/LubyRuffy/gptb2o"
	"github.com/LubyRuffy/gptb2o/auth"
	"github.com/LubyRuffy/gptb2o/backend"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const defaultAgentName = "gptb2o-adk"

func main() {
	var (
		model           = flag.String("model", gptb2o.DefaultModelFullID, "model id (supports legacy opencode/codex/*)")
		input           = flag.String("input", "你好，介绍一下你自己", "user input")
		image           = flag.String("image", "", "image input: local file path, http(s) URL, or data URL")
		imageDetail     = flag.String("image-detail", "", "image detail: auto|low|high")
		backendURL      = flag.String("backend-url", "", "chatgpt backend responses url (default: https://chatgpt.com/backend-api/codex/responses)")
		authSource      = flag.String("auth-source", "codex", "auth source: codex|opencode|env|auto")
		originator      = flag.String("originator", "", "Originator/User-Agent header (default: codex_cli_rs)")
		instructions    = flag.String("instructions", backend.DefaultInstructions, "system instructions for the model")
		reasoningEffort = flag.String("reasoning-effort", "", "reasoning effort forwarded to backend (e.g. low|medium|high)")
		noTools         = flag.Bool("no-tools", false, "disable built-in tools (bash)")
	)
	flag.Parse()

	provider, err := auth.NewProvider(*authSource)
	if err != nil {
		log.Fatalf("invalid auth-source: %v", err)
	}

	accessToken, accountID, err := provider.Auth(context.Background())
	if err != nil {
		log.Fatalf("auth failed: %v", err)
	}

	m, err := backend.NewChatModel(backend.ChatModelConfig{
		Model:           gptb2o.NormalizeModelID(*model),
		BackendURL:      firstNonEmpty(*backendURL, gptb2o.DefaultBackendURL),
		AccessToken:     accessToken,
		AccountID:       accountID,
		Originator:      firstNonEmpty(*originator, gptb2o.DefaultOriginator),
		Instructions:    *instructions,
		ReasoningEffort: *reasoningEffort,
	})
	if err != nil {
		log.Fatalf("create model failed: %v", err)
	}

	agentCfg := &adk.ChatModelAgentConfig{
		Name:        defaultAgentName,
		Description: "A chat model agent that uses the gptb2o model to generate responses.",
		Model:       m,
	}
	if !*noTools {
		agentCfg.ToolsConfig = adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{newBashTool()},
			},
		}
	}

	agent, err := adk.NewChatModelAgent(context.Background(), agentCfg)
	if err != nil {
		log.Fatalf("create agent failed: %v", err)
	}

	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: false,
	})

	userMessage, err := buildUserMessage(*input, *image, *imageDetail)
	if err != nil {
		log.Fatalf("build input failed: %v", err)
	}

	iter := runner.Run(context.Background(), []adk.Message{userMessage})
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			log.Fatalf("run failed: %v", ev.Err)
		}
		if ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		msg := ev.Output.MessageOutput.Message
		if msg == nil {
			continue
		}
		if msg.Content != "" {
			fmt.Print(msg.Content)
		}
	}
	fmt.Println()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func buildUserMessage(text, imageInput, imageDetail string) (*schema.Message, error) {
	imageInput = strings.TrimSpace(imageInput)
	text = strings.TrimSpace(text)
	if imageInput == "" {
		if text == "" {
			return nil, fmt.Errorf("input text is required when image is not provided")
		}
		return schema.UserMessage(text), nil
	}

	detail, err := parseImageDetail(imageDetail)
	if err != nil {
		return nil, err
	}

	imagePart, err := buildImagePart(imageInput, detail)
	if err != nil {
		return nil, err
	}

	parts := make([]schema.MessageInputPart, 0, 2)
	if text != "" {
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeText,
			Text: text,
		})
	}
	parts = append(parts, imagePart)

	return &schema.Message{
		Role:                  schema.User,
		UserInputMultiContent: parts,
	}, nil
}

func parseImageDetail(detail string) (schema.ImageURLDetail, error) {
	switch strings.ToLower(strings.TrimSpace(detail)) {
	case "":
		return "", nil
	case string(schema.ImageURLDetailAuto):
		return schema.ImageURLDetailAuto, nil
	case string(schema.ImageURLDetailLow):
		return schema.ImageURLDetailLow, nil
	case string(schema.ImageURLDetailHigh):
		return schema.ImageURLDetailHigh, nil
	default:
		return "", fmt.Errorf("invalid image-detail: %s (allowed: auto|low|high)", detail)
	}
}

func buildImagePart(imageInput string, detail schema.ImageURLDetail) (schema.MessageInputPart, error) {
	image := &schema.MessageInputImage{
		Detail: detail,
	}
	if isRemoteOrDataURL(imageInput) {
		image.URL = &imageInput
		return schema.MessageInputPart{
			Type:  schema.ChatMessagePartTypeImageURL,
			Image: image,
		}, nil
	}

	data, err := os.ReadFile(imageInput)
	if err != nil {
		return schema.MessageInputPart{}, fmt.Errorf("read image file failed: %w", err)
	}
	if len(data) == 0 {
		return schema.MessageInputPart{}, fmt.Errorf("image file is empty: %s", imageInput)
	}

	mimeType := detectMimeType(imageInput, data)
	encoded := base64.StdEncoding.EncodeToString(data)
	image.Base64Data = &encoded
	image.MIMEType = mimeType

	return schema.MessageInputPart{
		Type:  schema.ChatMessagePartTypeImageURL,
		Image: image,
	}, nil
}

func isRemoteOrDataURL(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:")
}

func detectMimeType(path string, data []byte) string {
	if ext := strings.ToLower(filepath.Ext(path)); ext != "" {
		if byExt := mime.TypeByExtension(ext); byExt != "" {
			if mt := strings.TrimSpace(strings.Split(byExt, ";")[0]); mt != "" {
				return mt
			}
		}
	}
	if detected := strings.TrimSpace(http.DetectContentType(data)); detected != "" {
		return detected
	}
	return "application/octet-stream"
}
