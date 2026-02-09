package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/LubyRuffy/gptb2o/auth"
	"github.com/LubyRuffy/gptb2o/openaihttp"
	"github.com/gin-gonic/gin"
)

func main() {
	var (
		listen     = flag.String("listen", "127.0.0.1:8080", "listen address")
		basePath   = flag.String("base-path", "/v1", "base path prefix")
		backendURL = flag.String("backend-url", "", "chatgpt backend responses url (default: https://chatgpt.com/backend-api/codex/responses)")
		authSource = flag.String("auth-source", "codex", "auth source: codex|opencode|env|auto")
		originator = flag.String("originator", "", "Originator/User-Agent header (default: codex_cli_rs)")
	)
	flag.Parse()

	provider, err := auth.NewProvider(*authSource)
	if err != nil {
		log.Fatalf("invalid auth-source: %v", err)
	}

	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	err = openaihttp.RegisterGinRoutes(r, openaihttp.Config{
		BasePath:   *basePath,
		BackendURL: *backendURL,
		Originator: *originator,
		AuthProvider: func(ctx context.Context) (string, string, error) {
			return provider.Auth(ctx)
		},
	})
	if err != nil {
		log.Fatalf("register routes failed: %v", err)
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("gptb2o server listening on http://%s%s", *listen, *basePath)
	log.Printf("try: curl http://%s%s/models", *listen, *basePath)
	log.Printf("try: curl http://%s%s/responses -H 'Content-Type: application/json' -d '{\"model\":\"chatgpt/codex/gpt-5.1\",\"input\":\"hi\",\"stream\":false}'", *listen, *basePath)
	log.Printf("OpenAI SDK base_url: http://%s%s", *listen, *basePath)

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Println(err)
	}
}
