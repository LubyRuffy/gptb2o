package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LubyRuffy/gptb2o"
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

	// `--listen :12345`/`0.0.0.0:12345`/`[::]:12345` binds to all interfaces; for curl examples
	// we prefer a localhost address that works for most users.
	exampleAddr := addrForLocalClient(*listen)
	log.Printf("gptb2o server listening on http://%s%s", exampleAddr, *basePath)
	log.Printf("try: curl http://%s%s/models", exampleAddr, *basePath)
	log.Printf("try: curl http://%s%s/responses -H 'Content-Type: application/json' -d '{\"model\":\"%s\",\"input\":\"hi\",\"stream\":false}'", exampleAddr, *basePath, gptb2o.DefaultModelFullID)
	log.Printf("OpenAI SDK base_url: http://%s%s", exampleAddr, *basePath)

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Println(err)
	}
}

func addrForLocalClient(listen string) string {
	listen = strings.TrimSpace(listen)
	host, port, ok := splitHostPortLoose(listen)
	if !ok {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func splitHostPortLoose(addr string) (host, port string, ok bool) {
	host, port, err := net.SplitHostPort(addr)
	if err == nil {
		return host, port, true
	}
	// Accept bracketless IPv6 like "::1:8080" by splitting on the last ':' if port is numeric.
	last := strings.LastIndex(addr, ":")
	if last <= 0 || last+1 >= len(addr) {
		return "", "", false
	}
	host = addr[:last]
	port = addr[last+1:]
	if _, err := strconv.Atoi(port); err != nil {
		return "", "", false
	}
	return host, port, true
}
