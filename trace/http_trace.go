package trace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const defaultMaxBodyBytes = 64 << 10

type TracerOptions struct {
	MaxBodyBytes int
	Logger       *log.Logger
}

type Tracer struct {
	store        *Store
	maxBodyBytes int
	logger       *log.Logger
}

func NewTracer(store *Store, opts TracerOptions) *Tracer {
	maxBodyBytes := opts.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = defaultMaxBodyBytes
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}
	return &Tracer{
		store:        store,
		maxBodyBytes: maxBodyBytes,
		logger:       logger,
	}
}

func (t *Tracer) WrapHTTP(next http.Handler) http.Handler {
	if next == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "handler is nil", http.StatusInternalServerError)
		})
	}
	if t == nil || t.store == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		interactionID := "ia_" + uuid.NewString()
		bodyBytes := readAndReplaceRequestBody(r)
		model, stream := extractModelAndStream(bodyBytes)

		_ = t.store.StartInteraction(Interaction{
			InteractionID: interactionID,
			Method:        r.Method,
			Path:          r.URL.Path,
			Query:         r.URL.RawQuery,
			ClientAPI:     detectClientAPI(r),
			Model:         model,
			Stream:        stream,
			StartedAt:     startedAt,
		})

		requestBody, requestTruncated := TruncateBody(bodyBytes, t.maxBodyBytes)
		_ = t.store.AppendEvent(InteractionEvent{
			InteractionID: interactionID,
			Kind:          EventClientRequest,
			Method:        r.Method,
			Path:          r.URL.Path,
			URL:           r.URL.String(),
			ContentType:   r.Header.Get("Content-Type"),
			HeadersJSON:   headersJSON(r.Header),
			Body:          requestBody,
			BodyTruncated: requestTruncated,
			Summary:       summarizeRequest(r.Method, r.URL.Path, model, stream, len(bodyBytes)),
			DurationMs:    0,
		})
		t.logf("interaction_id=%s stage=%s method=%s path=%s model=%s stream=%t", interactionID, EventClientRequest, r.Method, r.URL.Path, model, stream)

		ctx := ContextWithInteractionID(r.Context(), interactionID)
		r = r.WithContext(ctx)

		capture := newResponseCaptureWriter(w, t.maxBodyBytes)
		capture.Header().Set(InteractionIDHeader, interactionID)
		next.ServeHTTP(capture, r)

		responseHeaders := capture.Header().Clone()
		responseBody, responseTruncated := capture.body()
		_ = t.store.AppendEvent(InteractionEvent{
			InteractionID: interactionID,
			Kind:          EventClientResponse,
			Method:        r.Method,
			Path:          r.URL.Path,
			URL:           r.URL.String(),
			StatusCode:    capture.StatusCode(),
			ContentType:   responseHeaders.Get("Content-Type"),
			HeadersJSON:   headersJSON(responseHeaders),
			Body:          responseBody,
			BodyTruncated: responseTruncated,
			Summary:       summarizeResponse("client response", capture.StatusCode(), len(responseBody), responseTruncated),
			DurationMs:    time.Since(startedAt).Milliseconds(),
		})
		_ = t.store.FinishInteraction(interactionID, capture.StatusCode(), summarizeResponseError(responseHeaders.Get("Content-Type"), capture.StatusCode(), responseBody))
		t.logf("interaction_id=%s stage=%s status=%d duration_ms=%d", interactionID, EventClientResponse, capture.StatusCode(), time.Since(startedAt).Milliseconds())
	})
}

func (t *Tracer) logf(format string, args ...any) {
	if t == nil || t.logger == nil {
		return
	}
	t.logger.Printf("[gptb2o][trace] "+format, args...)
}

func readAndReplaceRequestBody(r *http.Request) []byte {
	if r == nil || r.Body == nil {
		return nil
	}
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		r.Body = http.NoBody
		return nil
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	return bodyBytes
}

func detectClientAPI(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	if strings.TrimSpace(r.Header.Get("anthropic-version")) != "" || strings.Contains(r.URL.Path, "/messages") {
		return "claude"
	}
	if strings.Contains(r.URL.Path, "/responses") || strings.Contains(r.URL.Path, "/chat/completions") {
		return "openai"
	}
	return "unknown"
}

func extractModelAndStream(body []byte) (string, bool) {
	if len(body) == 0 {
		return "", false
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", false
	}
	model, _ := payload["model"].(string)
	stream, _ := payload["stream"].(bool)
	return strings.TrimSpace(model), stream
}

func summarizeRequest(method, path, model string, stream bool, bodyLen int) string {
	return strings.TrimSpace(
		method + " " + path + " model=" + model + " stream=" + strconv.FormatBool(stream) + " body_bytes=" + strconv.Itoa(bodyLen),
	)
}

func summarizeResponse(prefix string, statusCode int, bodyLen int, truncated bool) string {
	summary := prefix + " status=" + strconv.Itoa(statusCode) + " body_bytes=" + strconv.Itoa(bodyLen)
	if truncated {
		summary += " truncated=true"
	}
	return summary
}

func summarizeResponseError(contentType string, statusCode int, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		if msg := extractSSEErrorSummary(body); msg != "" {
			return msg
		}
	}
	if statusCode < http.StatusBadRequest {
		return ""
	}
	if msg := extractJSONErrorMessage(body); msg != "" {
		return msg
	}
	firstLine := strings.TrimSpace(strings.SplitN(body, "\n", 2)[0])
	if firstLine == "" {
		return ""
	}
	return fmt.Sprintf("http %d: %s", statusCode, firstLine)
}

func extractSSEErrorSummary(body string) string {
	lines := strings.Split(body, "\n")
	var currentEvent string
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			continue
		}
		if currentEvent != "error" || !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if payload == "" {
			continue
		}
		var envelope struct {
			Type  string `json:"type"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
			continue
		}
		msg := strings.TrimSpace(envelope.Error.Message)
		errType := strings.TrimSpace(envelope.Error.Type)
		switch {
		case errType != "" && msg != "":
			return errType + ": " + msg
		case msg != "":
			return msg
		case errType != "":
			return errType
		}
	}
	return ""
}

func extractJSONErrorMessage(body string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return ""
	}
	if msg := strings.TrimSpace(jsonStringAtPath(payload, "error", "message")); msg != "" {
		return msg
	}
	if msg := strings.TrimSpace(jsonStringAtPath(payload, "message")); msg != "" {
		return msg
	}
	return ""
}

func jsonStringAtPath(payload map[string]any, path ...string) string {
	var current any = payload
	for _, key := range path {
		obj, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = obj[key]
	}
	value, _ := current.(string)
	return value
}

type limitedBuffer struct {
	maxBytes  int
	buf       bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) {
	if b.maxBytes <= 0 {
		if len(p) > 0 {
			b.truncated = true
		}
		return
	}
	remaining := b.maxBytes - b.buf.Len()
	if remaining <= 0 {
		if len(p) > 0 {
			b.truncated = true
		}
		return
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return
	}
	_, _ = b.buf.Write(p)
}

func (b *limitedBuffer) String() (string, bool) {
	return b.buf.String(), b.truncated
}

type responseCaptureWriter struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
	bodyBuffer  limitedBuffer
}

func newResponseCaptureWriter(w http.ResponseWriter, maxBodyBytes int) *responseCaptureWriter {
	return &responseCaptureWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
		bodyBuffer: limitedBuffer{
			maxBytes: maxBodyBytes,
		},
	}
}

func (w *responseCaptureWriter) Header() http.Header {
	return w.ResponseWriter.Header()
}

func (w *responseCaptureWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.statusCode = statusCode
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *responseCaptureWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	w.bodyBuffer.Write(p)
	return w.ResponseWriter.Write(p)
}

func (w *responseCaptureWriter) Flush() {
	flusher, ok := w.ResponseWriter.(http.Flusher)
	if ok {
		flusher.Flush()
	}
}

func (w *responseCaptureWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

func (w *responseCaptureWriter) StatusCode() int {
	return w.statusCode
}

func (w *responseCaptureWriter) body() (string, bool) {
	return w.bodyBuffer.String()
}
