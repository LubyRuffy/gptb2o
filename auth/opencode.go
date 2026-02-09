package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type openCodeAuthFile struct {
	OpenAI struct {
		Access    string `json:"access"`
		AccountID string `json:"accountId"`
	} `json:"openai"`
}

func ReadOpenCodeAuthFromPath(path string) (accessToken, accountID string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("failed to read opencode auth file: %w", err)
	}

	var auth openCodeAuthFile
	if err := json.Unmarshal(data, &auth); err != nil {
		return "", "", fmt.Errorf("failed to parse opencode auth file: %w", err)
	}

	access := strings.TrimSpace(auth.OpenAI.Access)
	if access == "" {
		return "", "", fmt.Errorf("opencode auth missing openai.access")
	}

	return access, strings.TrimSpace(auth.OpenAI.AccountID), nil
}

func openCodeDefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "opencode", "auth.json"), nil
}

type openCodeProvider struct{}

func (p *openCodeProvider) Auth(ctx context.Context) (string, string, error) {
	path, err := openCodeDefaultPath()
	if err != nil {
		return "", "", err
	}
	return ReadOpenCodeAuthFromPath(path)
}
