package auth

import (
	"context"
	"fmt"
	"os"
	"strings"
)

const (
	EnvAccessToken = "GPTB2O_ACCESS_TOKEN"
	EnvAccountID   = "GPTB2O_ACCOUNT_ID"
)

type envProvider struct{}

func (p *envProvider) Auth(ctx context.Context) (string, string, error) {
	access := strings.TrimSpace(os.Getenv(EnvAccessToken))
	if access == "" {
		return "", "", fmt.Errorf("%s is not set", EnvAccessToken)
	}
	return access, strings.TrimSpace(os.Getenv(EnvAccountID)), nil
}
