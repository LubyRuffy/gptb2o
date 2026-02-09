package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/LubyRuffy/gptb2o"
	"github.com/LubyRuffy/gptb2o/auth"
	"github.com/LubyRuffy/gptb2o/backend"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func main() {
	var (
		model      = flag.String("model", gptb2o.ModelNamespace+"gpt-5.1", "model id (supports legacy opencode/codex/*)")
		input      = flag.String("input", "你好，介绍一下你自己", "user input")
		backendURL = flag.String("backend-url", "", "chatgpt backend responses url (default: https://chatgpt.com/backend-api/codex/responses)")
		authSource = flag.String("auth-source", "codex", "auth source: codex|opencode|env|auto")
		originator = flag.String("originator", "", "Originator/User-Agent header (default: codex_cli_rs)")
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
		Model:       gptb2o.NormalizeModelID(*model),
		BackendURL:  firstNonEmpty(*backendURL, gptb2o.DefaultBackendURL),
		AccessToken: accessToken,
		AccountID:   accountID,
		Originator:  firstNonEmpty(*originator, gptb2o.DefaultOriginator),
	})
	if err != nil {
		log.Fatalf("create model failed: %v", err)
	}

	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Model: m,
	})
	if err != nil {
		log.Fatalf("create agent failed: %v", err)
	}

	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: false,
	})

	iter := runner.Run(context.Background(), []adk.Message{schema.UserMessage(*input)})
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
