package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/LubyRuffy/gptb2o"
	"github.com/LubyRuffy/gptb2o/auth"
	"github.com/LubyRuffy/gptb2o/openaihttp"
	"github.com/LubyRuffy/gptb2o/trace"
	"github.com/gin-gonic/gin"
)

var defaultTraceDBPath = filepath.Join("artifacts", "traces", "gptb2o-trace.db")

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	var (
		flagSet         = flag.NewFlagSet("gptb2o-server", flag.ContinueOnError)
		listen          = flagSet.String("listen", "127.0.0.1:12345", "listen address")
		basePath        = flagSet.String("base-path", "/v1", "base path prefix")
		backendURL      = flagSet.String("backend-url", "", "chatgpt backend responses url (default: https://chatgpt.com/backend-api/codex/responses)")
		authSource      = flagSet.String("auth-source", "codex", "auth source: codex|opencode|env|auto")
		originator      = flagSet.String("originator", "", "Originator/User-Agent header (default: codex_cli_rs)")
		reasoningEffort = flagSet.String("reasoning-effort", "", "default reasoning effort forwarded to backend (e.g. low|medium|high)")
		traceDBPath     = flagSet.String("trace-db-path", defaultTraceDBPath, "sqlite path for full-chain tracing")
		traceMaxBody    = flagSet.Int("trace-max-body-bytes", 64<<10, "max body bytes stored per trace event")
		showInteraction = flagSet.String("show-interaction", "", "print a traced interaction by id and exit")
	)
	flagSet.SetOutput(io.Discard)
	if err := flagSet.Parse(args); err != nil {
		return err
	}

	tracePath := strings.TrimSpace(*traceDBPath)
	if tracePath == "" {
		tracePath = defaultTraceDBPath
	}

	store, err := trace.OpenStore(tracePath)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	tracer := trace.NewTracer(store, trace.TracerOptions{MaxBodyBytes: *traceMaxBody})
	if interactionID := strings.TrimSpace(*showInteraction); interactionID != "" {
		interaction, events, err := store.GetInteraction(interactionID)
		if err != nil {
			return err
		}
		_, err = io.WriteString(stdout, trace.FormatInteractionReport(interaction, events))
		return err
	}

	provider, err := auth.NewProvider(*authSource)
	if err != nil {
		return fmt.Errorf("invalid auth-source: %w", err)
	}

	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	err = openaihttp.RegisterGinRoutes(r, openaihttp.Config{
		BasePath:        *basePath,
		BackendURL:      *backendURL,
		Originator:      *originator,
		ReasoningEffort: *reasoningEffort,
		Tracer:          tracer,
		AuthProvider: func(ctx context.Context) (string, string, error) {
			return provider.Auth(ctx)
		},
	})
	if err != nil {
		return fmt.Errorf("register routes failed: %w", err)
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
	log.Printf("try: curl http://%s%s/messages -H 'Content-Type: application/json' -d '{\"model\":\"%s\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"stream\":false}'", exampleAddr, *basePath, gptb2o.DefaultModelFullID)
	log.Printf("Claude Code base_url: http://%s%s", exampleAddr, *basePath)
	log.Printf("trace db: %s", tracePath)

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
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
