package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type codexAuthFile struct {
	OpenAIAPIKey string `json:"OPENAI_API_KEY"`
	Tokens       struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

func ReadCodexAuthFromPath(path string) (accessToken, accountID string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("failed to read codex auth file: %w", err)
	}

	var auth codexAuthFile
	if err := json.Unmarshal(data, &auth); err != nil {
		return "", "", fmt.Errorf("failed to parse codex auth file: %w", err)
	}

	access := strings.TrimSpace(auth.Tokens.AccessToken)
	if access == "" {
		// 兼容某些场景下只有 OPENAI_API_KEY 的情况（仍当作 bearer token 使用）。
		access = strings.TrimSpace(auth.OpenAIAPIKey)
	}
	if access == "" {
		return "", "", fmt.Errorf("codex auth missing tokens.access_token")
	}

	return access, strings.TrimSpace(auth.Tokens.AccountID), nil
}

func codexDefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	return filepath.Join(home, ".codex", "auth.json"), nil
}

type codexProvider struct{}

func (p *codexProvider) Auth(ctx context.Context) (string, string, error) {
	path, err := codexDefaultPath()
	if err != nil {
		return "", "", err
	}
	return ReadCodexAuthFromPath(path)
}
