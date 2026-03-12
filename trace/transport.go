package trace

import (
	"io"
	"net/http"
	"sync"
	"time"
)

func (t *Tracer) WrapTransport(base http.RoundTripper) http.RoundTripper {
	if t == nil || t.store == nil {
		if base == nil {
			return http.DefaultTransport
		}
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		interactionID := InteractionIDFromContext(req.Context())
		if interactionID == "" {
			return base.RoundTrip(req)
		}

		startedAt := time.Now()
		bodyBytes := readAndReplaceRequestBody(req)
		requestBody, requestTruncated := TruncateBody(bodyBytes, t.maxBodyBytes)
		_ = t.store.AppendEvent(InteractionEvent{
			InteractionID: interactionID,
			Kind:          EventBackendRequest,
			Method:        req.Method,
			Path:          req.URL.Path,
			URL:           req.URL.String(),
			ContentType:   req.Header.Get("Content-Type"),
			HeadersJSON:   headersJSON(req.Header),
			Body:          requestBody,
			BodyTruncated: requestTruncated,
			Summary:       summarizeRequest(req.Method, req.URL.Path, "", false, len(bodyBytes)),
		})
		t.logf("interaction_id=%s stage=%s method=%s url=%s", interactionID, EventBackendRequest, req.Method, req.URL.String())

		resp, err := base.RoundTrip(req)
		if err != nil {
			_ = t.store.AppendEvent(InteractionEvent{
				InteractionID: interactionID,
				Kind:          EventBackendResponse,
				Method:        req.Method,
				Path:          req.URL.Path,
				URL:           req.URL.String(),
				Summary:       "backend response error: " + err.Error(),
				DurationMs:    time.Since(startedAt).Milliseconds(),
			})
			return nil, err
		}
		if resp.Body == nil {
			_ = t.store.AppendEvent(InteractionEvent{
				InteractionID: interactionID,
				Kind:          EventBackendResponse,
				Method:        req.Method,
				Path:          req.URL.Path,
				URL:           req.URL.String(),
				StatusCode:    resp.StatusCode,
				ContentType:   resp.Header.Get("Content-Type"),
				HeadersJSON:   headersJSON(resp.Header),
				Summary:       summarizeResponse("backend response", resp.StatusCode, 0, false),
				DurationMs:    time.Since(startedAt).Milliseconds(),
			})
			return resp, nil
		}

		resp.Body = &responseBodyRecorder{
			ReadCloser:    resp.Body,
			tracer:        t,
			interactionID: interactionID,
			method:        req.Method,
			path:          req.URL.Path,
			url:           req.URL.String(),
			statusCode:    resp.StatusCode,
			contentType:   resp.Header.Get("Content-Type"),
			headersJSON:   headersJSON(resp.Header),
			startedAt:     startedAt,
			maxBodyBytes:  t.maxBodyBytes,
		}
		return resp, nil
	})
}

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type responseBodyRecorder struct {
	io.ReadCloser
	tracer        *Tracer
	interactionID string
	method        string
	path          string
	url           string
	statusCode    int
	contentType   string
	headersJSON   string
	startedAt     time.Time
	maxBodyBytes  int
	bodyBuffer    limitedBuffer
	once          sync.Once
}

func (r *responseBodyRecorder) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.bodyBuffer.maxBytes = r.maxBodyBytes
		r.bodyBuffer.Write(p[:n])
	}
	if err != nil {
		r.record()
	}
	return n, err
}

func (r *responseBodyRecorder) Close() error {
	err := r.ReadCloser.Close()
	r.record()
	return err
}

func (r *responseBodyRecorder) record() {
	r.once.Do(func() {
		body, truncated := r.bodyBuffer.String()
		_ = r.tracer.store.AppendEvent(InteractionEvent{
			InteractionID: r.interactionID,
			Kind:          EventBackendResponse,
			Method:        r.method,
			Path:          r.path,
			URL:           r.url,
			StatusCode:    r.statusCode,
			ContentType:   r.contentType,
			HeadersJSON:   r.headersJSON,
			Body:          body,
			BodyTruncated: truncated,
			Summary:       summarizeResponse("backend response", r.statusCode, len(body), truncated),
			DurationMs:    time.Since(r.startedAt).Milliseconds(),
		})
		r.tracer.logf("interaction_id=%s stage=%s status=%d duration_ms=%d", r.interactionID, EventBackendResponse, r.statusCode, time.Since(r.startedAt).Milliseconds())
	})
}
