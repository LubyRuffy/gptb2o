package openaihttp

import (
	"fmt"
	"net/http"
	"strings"

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
	claudeModelsHandler := ClaudeModelsListHandler()
	r.GET(joinPath(basePath, "/models"), gin.WrapF(func(w http.ResponseWriter, req *http.Request) {
		if isClaudeAPIRequest(req) {
			claudeModelsHandler(w, req)
			return
		}
		modelsHandler(w, req)
	}))
	r.GET(joinPath(basePath, "/models/:model_id"), func(c *gin.Context) {
		if !isClaudeAPIRequest(c.Request) {
			// 为避免影响 OpenAI 客户端，非 Claude 请求保持与 gin 默认行为一致（404 文本）。
			c.String(http.StatusNotFound, "404 page not found")
			return
		}

		modelID := strings.TrimSpace(c.Param("model_id"))
		if modelID == "" {
			writeClaudeError(c.Writer, http.StatusBadRequest, "model_id is required")
			return
		}
		// 尽量复用与 /v1/messages 同一套“支持模型”判定，避免 models 与 messages 不一致。
		if _, err := resolveClaudeModelID(modelID); err != nil {
			writeClaudeError(c.Writer, http.StatusNotFound, "model not found")
			return
		}
		writeJSON(c.Writer, claudeModelInfoForID(modelID))
	})
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
