package auth

import (
	"context"
	"fmt"
	"strings"
)

// NewProvider 根据来源创建 Provider。
// source 允许：codex/opencode/env/auto；空值按 codex 处理。
func NewProvider(source string) (Provider, error) {
	s := strings.ToLower(strings.TrimSpace(source))
	if s == "" {
		s = string(SourceCodex)
	}
	switch Source(s) {
	case SourceCodex:
		return &codexProvider{}, nil
	case SourceOpenCode:
		return &openCodeProvider{}, nil
	case SourceEnv:
		return &envProvider{}, nil
	case SourceAuto:
		return &autoProvider{providers: []Provider{&codexProvider{}, &openCodeProvider{}, &envProvider{}}}, nil
	default:
		return nil, fmt.Errorf("unsupported auth source: %s", source)
	}
}

type autoProvider struct {
	providers []Provider
}

func (p *autoProvider) Auth(ctx context.Context) (string, string, error) {
	var lastErr error
	for _, provider := range p.providers {
		access, account, err := provider.Auth(ctx)
		if err == nil && strings.TrimSpace(access) != "" {
			return access, account, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr != nil {
		return "", "", lastErr
	}
	return "", "", fmt.Errorf("no auth available")
}
