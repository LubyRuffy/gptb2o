package openaihttp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/LubyRuffy/gptb2o"
	"github.com/LubyRuffy/gptb2o/backend"
	"github.com/LubyRuffy/gptb2o/openaiapi"
)

const defaultSystemFingerprint = "fp_gptb2o"

func Handlers(cfg Config) (modelsHandler http.HandlerFunc, chatHandler http.HandlerFunc, responsesHandler http.HandlerFunc, err error) {
	resolved, err := resolveConfig(cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	chatModelFactory := newChatModelFactory(resolved)

	compat, err := newCompatHandler(compatConfig{
		Now:               time.Now,
		NewChatCompletion: openaiapi.NewChatCompletionID,
		WriteJSON:         writeJSON,
		WriteOpenAIError:  writeOpenAIError,
		SystemFingerprint: resolved.SystemFingerprint,
		NewChatModel:      chatModelFactory,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	modelsHandler = compat.handleModels
	chatHandler = compat.handleChatCompletions
	responsesHandler = newResponsesHandler(resolved)
	return modelsHandler, chatHandler, responsesHandler, nil
}

func ClaudeMessagesHandler(cfg Config) (http.HandlerFunc, error) {
	resolved, err := resolveConfig(cfg)
	if err != nil {
		return nil, err
	}
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now:          time.Now,
		NewChatModel: newChatModelFactory(resolved),
		WriteJSON:    writeJSON,
		WriteError:   writeClaudeError,
	})
	if err != nil {
		return nil, err
	}
	return h.handleMessages, nil
}

func newChatModelFactory(resolved resolvedConfig) func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
	return func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
		accessToken, accountID, err := resolved.AuthProvider(ctx)
		if err != nil {
			return nil, &httpError{
				Status:  http.StatusServiceUnavailable,
				Message: "auth not available",
				Err:     err,
			}
		}

		m, err := backend.NewChatModel(backend.ChatModelConfig{
			Model:       modelID,
			BackendURL:  resolved.BackendURL,
			AccessToken: accessToken,
			AccountID:   accountID,
			HTTPClient:  resolved.HTTPClient,
			Originator:  resolved.Originator,
		})
		if err != nil {
			return nil, &httpError{
				Status:  http.StatusInternalServerError,
				Message: "failed to create backend model",
				Err:     err,
			}
		}

		nativeTools := backend.NativeToolsFromOpenAITools(tools)
		if len(nativeTools) > 0 {
			m = m.WithNativeTools(nativeTools)
		}
		functionTools := backend.FunctionToolsFromOpenAITools(tools)
		if len(functionTools) > 0 {
			m = m.WithFunctionTools(functionTools)
		}
		if toolCallHandler != nil {
			m = m.WithToolCallHandler(toolCallHandler)
		}
		return m, nil
	}
}

type resolvedConfig struct {
	BasePath          string
	BackendURL        string
	HTTPClient        *http.Client
	AuthProvider      AuthProvider
	Originator        string
	SystemFingerprint string
}

func resolveConfig(cfg Config) (resolvedConfig, error) {
	if cfg.AuthProvider == nil {
		return resolvedConfig{}, fmt.Errorf("AuthProvider is required")
	}

	backendURL := strings.TrimSpace(cfg.BackendURL)
	if backendURL == "" {
		backendURL = gptb2o.DefaultBackendURL
	}

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{}
	}

	originator := strings.TrimSpace(cfg.Originator)
	if originator == "" {
		originator = gptb2o.DefaultOriginator
	}

	fp := strings.TrimSpace(cfg.SystemFingerprint)
	if fp == "" {
		fp = defaultSystemFingerprint
	}

	return resolvedConfig{
		BasePath:          normalizeBasePath(cfg.BasePath),
		BackendURL:        backendURL,
		HTTPClient:        client,
		AuthProvider:      cfg.AuthProvider,
		Originator:        originator,
		SystemFingerprint: fp,
	}, nil
}
