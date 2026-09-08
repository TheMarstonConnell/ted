package agent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// The Codex CLI keeps its ChatGPT login in $CODEX_HOME/auth.json, which
// defaults to ~/.codex/auth.json. The file holds an access token, a refresh
// token, an identity token, and the account the login belongs to. Access
// tokens are short-lived, so the CLI refreshes them against the OpenAI OAuth
// token endpoint with its own client id and writes the new tokens back. This
// store reproduces that behaviour so the harness can reuse an existing login.
const (
	codexHomeVariable          = "CODEX_HOME"
	codexAuthFileName          = "auth.json"
	codexOAuthClientId         = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexTokenRefreshURL       = "https://auth.openai.com/oauth/token"
	codexAccessTokenExpiryLead = 5 * time.Minute
	codexTokenRefreshInterval  = 8 * 24 * time.Hour
)

type codexTokens struct {
	IdToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountId    string `json:"account_id,omitempty"`
}

// CodexCredentials is what one request needs: a bearer token and the account
// the request is billed to.
type CodexCredentials struct {
	AccessToken string
	AccountId   string
}

// CodexAuthStore reads and refreshes the Codex CLI login file. Fields the
// harness does not understand are preserved on write so the CLI keeps working.
type CodexAuthStore struct {
	path   string
	client *http.Client
	mu     sync.Mutex
}

// DefaultCodexAuthPath reports where the Codex CLI keeps its login.
func DefaultCodexAuthPath() (string, error) {
	if home := os.Getenv(codexHomeVariable); home != "" {
		return filepath.Join(home, codexAuthFileName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not resolve home directory: %w", err)
	}
	return filepath.Join(home, ".codex", codexAuthFileName), nil
}

func NewCodexAuthStore(path string) *CodexAuthStore {
	return &CodexAuthStore{
		path:   path,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Exists reports whether a login file is present at all.
func (s *CodexAuthStore) Exists() bool {
	_, err := os.Stat(s.path)
	return err == nil
}

// Credentials returns a usable access token, refreshing it first when it is
// about to expire or has not been refreshed for the interval the CLI uses.
func (s *CodexAuthStore) Credentials(logger *zap.Logger) (*CodexCredentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	file, tokens, err := s.load()
	if err != nil {
		return nil, err
	}

	if shouldRefresh(file, tokens, time.Now()) {
		logger.Debug("codex access token is stale, refreshing")
		tokens, err = s.refreshAndSave(logger, file, tokens)
		if err != nil {
			return nil, err
		}
	}

	return credentialsFrom(tokens)
}

// Refresh discards the current access token and obtains a new one. It is the
// recovery path when the backend rejects a token the store believed valid.
func (s *CodexAuthStore) Refresh(logger *zap.Logger) (*CodexCredentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	file, tokens, err := s.load()
	if err != nil {
		return nil, err
	}

	tokens, err = s.refreshAndSave(logger, file, tokens)
	if err != nil {
		return nil, err
	}

	return credentialsFrom(tokens)
}

// load reads the file as a generic object so unknown fields survive a write,
// and decodes the tokens block that the store actually works with.
func (s *CodexAuthStore) load() (map[string]json.RawMessage, *codexTokens, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, nil, fmt.Errorf("could not read codex auth file %s: %w", s.path, err)
	}

	file := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, nil, fmt.Errorf("could not parse codex auth file %s: %w", s.path, err)
	}

	rawTokens, ok := file["tokens"]
	if !ok || string(rawTokens) == "null" {
		return nil, nil, fmt.Errorf("codex auth file %s holds no ChatGPT login; run `codex login` first", s.path)
	}

	tokens := codexTokens{}
	if err := json.Unmarshal(rawTokens, &tokens); err != nil {
		return nil, nil, fmt.Errorf("could not parse codex tokens: %w", err)
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		return nil, nil, fmt.Errorf("codex auth file %s is missing an access or refresh token", s.path)
	}

	return file, &tokens, nil
}

func shouldRefresh(file map[string]json.RawMessage, tokens *codexTokens, now time.Time) bool {
	if expiry, ok := jwtExpiry(tokens.AccessToken); ok {
		return !expiry.After(now.Add(codexAccessTokenExpiryLead))
	}

	rawLastRefresh, ok := file["last_refresh"]
	if !ok {
		return false
	}
	var lastRefresh time.Time
	if err := json.Unmarshal(rawLastRefresh, &lastRefresh); err != nil {
		return false
	}
	return lastRefresh.Before(now.Add(-codexTokenRefreshInterval))
}

type codexRefreshRequest struct {
	ClientId     string `json:"client_id"`
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
}

type codexRefreshResponse struct {
	IdToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func (s *CodexAuthStore) refreshAndSave(logger *zap.Logger, file map[string]json.RawMessage, tokens *codexTokens) (*codexTokens, error) {
	bodyData, err := json.Marshal(codexRefreshRequest{
		ClientId:     codexOAuthClientId,
		GrantType:    "refresh_token",
		RefreshToken: tokens.RefreshToken,
	})
	if err != nil {
		return nil, fmt.Errorf("could not build token refresh request: %w", err)
	}

	req, err := http.NewRequest("POST", codexTokenRefreshURL, bytes.NewReader(bodyData))
	if err != nil {
		return nil, fmt.Errorf("could not build token refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("could not read token refresh response: %w", err)
	}

	logger.Debug("codex token refresh response", zap.Int("status_code", resp.StatusCode))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh returned status %d: %s; run `codex login` again if this persists", resp.StatusCode, body)
	}

	refreshed := codexRefreshResponse{}
	if err := json.Unmarshal(body, &refreshed); err != nil {
		return nil, fmt.Errorf("could not parse token refresh response: %w", err)
	}
	if refreshed.AccessToken == "" {
		return nil, errors.New("token refresh response carried no access token")
	}

	updated := *tokens
	updated.AccessToken = refreshed.AccessToken
	if refreshed.RefreshToken != "" {
		updated.RefreshToken = refreshed.RefreshToken
	}
	if refreshed.IdToken != "" {
		updated.IdToken = refreshed.IdToken
	}

	if err := s.save(file, &updated, time.Now().UTC()); err != nil {
		return nil, err
	}

	return &updated, nil
}

// save writes the file atomically with owner-only permissions, the same
// protection the CLI applies.
func (s *CodexAuthStore) save(file map[string]json.RawMessage, tokens *codexTokens, refreshedAt time.Time) error {
	rawTokens, err := json.Marshal(tokens)
	if err != nil {
		return fmt.Errorf("could not encode codex tokens: %w", err)
	}
	rawLastRefresh, err := json.Marshal(refreshedAt)
	if err != nil {
		return fmt.Errorf("could not encode refresh time: %w", err)
	}
	file["tokens"] = rawTokens
	file["last_refresh"] = rawLastRefresh

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("could not encode codex auth file: %w", err)
	}

	temp, err := os.CreateTemp(filepath.Dir(s.path), codexAuthFileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("could not create temporary auth file: %w", err)
	}
	tempPath := temp.Name()

	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		os.Remove(tempPath)
		return fmt.Errorf("could not restrict temporary auth file: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		os.Remove(tempPath)
		return fmt.Errorf("could not write temporary auth file: %w", err)
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("could not close temporary auth file: %w", err)
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("could not replace codex auth file: %w", err)
	}

	return nil
}

func credentialsFrom(tokens *codexTokens) (*CodexCredentials, error) {
	accountId := tokens.AccountId
	if accountId == "" {
		accountId = accountIdFromIdToken(tokens.IdToken)
	}
	if accountId == "" {
		return nil, errors.New("codex login carries no ChatGPT account id")
	}

	return &CodexCredentials{
		AccessToken: tokens.AccessToken,
		AccountId:   accountId,
	}, nil
}

// jwtClaims decodes the payload of a JSON Web Token without verifying its
// signature. The store only reads timing and account fields; the backend
// remains the authority on whether the token is valid.
func jwtClaims(token string) (map[string]json.RawMessage, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	claims := map[string]json.RawMessage{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, false
	}
	return claims, true
}

func jwtExpiry(token string) (time.Time, bool) {
	claims, ok := jwtClaims(token)
	if !ok {
		return time.Time{}, false
	}
	var exp int64
	if err := json.Unmarshal(claims["exp"], &exp); err != nil || exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(exp, 0), true
}

// The identity token names the ChatGPT account under an OpenAI-specific claim.
const codexAuthClaim = "https://api.openai.com/auth"

func accountIdFromIdToken(token string) string {
	claims, ok := jwtClaims(token)
	if !ok {
		return ""
	}
	var auth struct {
		ChatGPTAccountId string `json:"chatgpt_account_id"`
	}
	if err := json.Unmarshal(claims[codexAuthClaim], &auth); err != nil {
		return ""
	}
	return auth.ChatGPTAccountId
}
