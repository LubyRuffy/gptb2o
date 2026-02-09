package auth

import "context"

// Provider 用于从不同来源读取 access token / account id。
type Provider interface {
	Auth(ctx context.Context) (accessToken, accountID string, err error)
}

type Source string

const (
	SourceCodex    Source = "codex"
	SourceOpenCode Source = "opencode"
	SourceEnv      Source = "env"
	SourceAuto     Source = "auto"
)
