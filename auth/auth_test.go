package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadCodexAuthFromPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "auth.json")
	require.NoError(t, os.WriteFile(p, []byte(`{
  "OPENAI_API_KEY": "k_fallback",
  "tokens": {
    "access_token": "k_access",
    "account_id": "acc_1"
  }
}`), 0o600))

	access, account, err := ReadCodexAuthFromPath(p)
	require.NoError(t, err)
	require.Equal(t, "k_access", access)
	require.Equal(t, "acc_1", account)
}

func TestReadOpenCodeAuthFromPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "auth.json")
	require.NoError(t, os.WriteFile(p, []byte(`{
  "openai": {
    "access": "k_access",
    "accountId": "acc_2"
  }
}`), 0o600))

	access, account, err := ReadOpenCodeAuthFromPath(p)
	require.NoError(t, err)
	require.Equal(t, "k_access", access)
	require.Equal(t, "acc_2", account)
}

func TestEnvProvider(t *testing.T) {
	t.Setenv(EnvAccessToken, "k_access")
	t.Setenv(EnvAccountID, "acc_3")

	p := &envProvider{}
	access, account, err := p.Auth(context.Background())
	require.NoError(t, err)
	require.Equal(t, "k_access", access)
	require.Equal(t, "acc_3", account)
}

func TestNewProvider_Auto(t *testing.T) {
	// 隔离真实 HOME，避免读取到开发机上的 ~/.codex/auth.json 导致泄漏与不稳定。
	t.Setenv("HOME", t.TempDir())
	t.Setenv(EnvAccessToken, "k_access")

	p, err := NewProvider("auto")
	require.NoError(t, err)
	access, _, err := p.Auth(context.Background())
	require.NoError(t, err)
	require.Equal(t, "k_access", access)
}
