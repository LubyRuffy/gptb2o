package openaihttp

import (
	"fmt"

	"github.com/gin-gonic/gin"
)

func RegisterGinRoutes(r gin.IRouter, cfg Config) error {
	if r == nil {
		return fmt.Errorf("router is nil")
	}
	modelsHandler, chatHandler, responsesHandler, err := Handlers(cfg)
	if err != nil {
		return err
	}

	basePath := normalizeBasePath(cfg.BasePath)
	r.GET(joinPath(basePath, "/models"), gin.WrapF(modelsHandler))
	r.POST(joinPath(basePath, "/chat/completions"), gin.WrapF(chatHandler))
	r.POST(joinPath(basePath, "/responses"), gin.WrapF(responsesHandler))
	claudeHandler, err := ClaudeMessagesHandler(cfg)
	if err != nil {
		return err
	}
	r.POST(joinPath(basePath, "/messages"), gin.WrapF(claudeHandler))
	claudeCountTokensHandler, err := ClaudeCountTokensHandler(cfg)
	if err != nil {
		return err
	}
	r.POST(joinPath(basePath, "/messages/count_tokens"), gin.WrapF(claudeCountTokensHandler))
	return nil
}
