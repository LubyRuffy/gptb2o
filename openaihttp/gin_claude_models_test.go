package openaihttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LubyRuffy/gptb2o/openaihttp"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGin_ClaudeModels_ListAndGet_OK(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	require.NoError(t, openaihttp.RegisterGinRoutes(r, openaihttp.Config{
		BasePath: "/v1",
		AuthProvider: func(ctx context.Context) (string, string, error) {
			return "token", "", nil
		},
	}))

	// List models (Claude-style) by sending anthropic headers.
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("anthropic-version", "2023-06-01")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		FirstID string `json:"first_id"`
		HasMore bool   `json:"has_more"`
		LastID  string `json:"last_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.False(t, list.HasMore)
	require.NotEmpty(t, list.Data)
	require.Equal(t, list.Data[0].ID, list.FirstID)
	require.Equal(t, list.Data[len(list.Data)-1].ID, list.LastID)

	// Get model (Claude-style).
	req = httptest.NewRequest(http.MethodGet, "/v1/models/sonnet", nil)
	req.Header.Set("anthropic-version", "2023-06-01")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var got struct {
		Type        string `json:"type"`
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "model", got.Type)
	require.Equal(t, "sonnet", got.ID)
	require.Equal(t, "Sonnet", got.DisplayName)
}

func TestGin_ClaudeModels_Get_NonClaudeKeeps404Text(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	require.NoError(t, openaihttp.RegisterGinRoutes(r, openaihttp.Config{
		BasePath: "/v1",
		AuthProvider: func(ctx context.Context) (string, string, error) {
			return "token", "", nil
		},
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/models/sonnet", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "404 page not found")
}
