package oauth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ipfans/ccswap/pkg/models"
)

const (
	ClientID       = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	TokenURL       = "https://platform.claude.com/v1/oauth/token"
	UsageURL       = "https://api.anthropic.com/api/oauth/usage"
	BetaHeader     = "oauth-2025-04-20"
	ExpiryBufferMS = 5 * 60 * 1000 // 5 minutes in milliseconds
)

// ExtractAccessToken parses credsJSON into models.Credentials and returns the
// OAuth access token. It returns an error if the claudeAiOauth field is missing
// or the access token is empty.
func ExtractAccessToken(credsJSON string) (string, error) {
	var creds models.Credentials
	if err := json.Unmarshal([]byte(credsJSON), &creds); err != nil {
		return "", fmt.Errorf("parsing credentials: %w", err)
	}
	if creds.ClaudeAIOAuth == nil {
		return "", fmt.Errorf("credentials missing claudeAiOauth field")
	}
	if creds.ClaudeAIOAuth.AccessToken == "" {
		return "", fmt.Errorf("credentials missing access token")
	}
	return creds.ClaudeAIOAuth.AccessToken, nil
}

// ExtractOAuthData parses credsJSON into models.Credentials and returns the
// OAuthData pointer. It returns an error if the claudeAiOauth field is missing.
func ExtractOAuthData(credsJSON string) (*models.OAuthData, error) {
	var creds models.Credentials
	if err := json.Unmarshal([]byte(credsJSON), &creds); err != nil {
		return nil, fmt.Errorf("parsing credentials: %w", err)
	}
	if creds.ClaudeAIOAuth == nil {
		return nil, fmt.Errorf("credentials missing claudeAiOauth field")
	}
	return creds.ClaudeAIOAuth, nil
}

// IsTokenExpired returns true if expiresAt (unix milliseconds) is within the
// expiry buffer of the current time.
func IsTokenExpired(expiresAt int64) bool {
	nowMS := time.Now().UnixMilli()
	return expiresAt < nowMS+ExpiryBufferMS
}

// refreshRequest is the JSON body sent to the token endpoint.
type refreshRequest struct {
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
	ClientID     string `json:"client_id"`
}

// refreshResponse represents the token endpoint response.
type refreshResponse struct {
	AccessToken  string   `json:"access_token"`
	ExpiresIn    int64    `json:"expires_in"` // seconds
	RefreshToken string   `json:"refresh_token,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
}

// RefreshCredentials refreshes the OAuth token using the refresh_token found in
// credsJSON. It returns the updated full credentials JSON string with the new
// token values merged in, preserving any unknown fields. On HTTP error it
// returns empty string and nil (silent failure, matching Python behavior).
// If httpClient is nil, http.DefaultClient with a 10s timeout is used.
func RefreshCredentials(credsJSON string, httpClient *http.Client) (string, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	oauthData, err := ExtractOAuthData(credsJSON)
	if err != nil {
		return "", err
	}

	reqBody := refreshRequest{
		GrantType:    "refresh_token",
		RefreshToken: oauthData.RefreshToken,
		ClientID:     ClientID,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshaling refresh request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, TokenURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		// Network error — silent failure.
		return "", nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// HTTP error — silent failure matching Python behavior.
		return "", nil
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil
	}

	var tokenResp refreshResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return "", nil
	}

	// Re-parse original creds as a generic map to preserve unknown fields.
	var credsMap map[string]any
	dec := json.NewDecoder(bytes.NewReader([]byte(credsJSON)))
	dec.UseNumber()
	if err := dec.Decode(&credsMap); err != nil {
		return "", fmt.Errorf("re-parsing credentials map: %w", err)
	}

	oauthMap, ok := credsMap["claudeAiOauth"].(map[string]any)
	if !ok {
		oauthMap = make(map[string]any)
	}

	oauthMap["accessToken"] = tokenResp.AccessToken
	nowMS := time.Now().UnixMilli()
	oauthMap["expiresAt"] = json.Number(fmt.Sprintf("%d", nowMS+tokenResp.ExpiresIn*1000))
	if tokenResp.RefreshToken != "" {
		oauthMap["refreshToken"] = tokenResp.RefreshToken
	}
	if tokenResp.Scopes != nil {
		oauthMap["scopes"] = tokenResp.Scopes
	}

	credsMap["claudeAiOauth"] = oauthMap

	updatedJSON, err := json.Marshal(credsMap)
	if err != nil {
		return "", fmt.Errorf("marshaling updated credentials: %w", err)
	}
	return string(updatedJSON), nil
}

// FetchUsage queries the usage API and returns the parsed UsageResponse.
// If httpClient is nil, http.DefaultClient with a 5s timeout is used.
func FetchUsage(accessToken string, httpClient *http.Client) (*models.UsageResponse, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}

	req, err := http.NewRequest(http.MethodGet, UsageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating usage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", BetaHeader)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("usage request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("usage API returned status %d", resp.StatusCode)
	}

	var usage models.UsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&usage); err != nil {
		return nil, fmt.Errorf("decoding usage response: %w", err)
	}
	return &usage, nil
}

// FetchUsageForAccount fetches usage for a specific account. If the account is
// not active and the token is expired, it refreshes credentials first and calls
// persistFn to save the updated creds. On a 401 response (and not active), it
// refreshes once and retries.
func FetchUsageForAccount(
	accountNum int,
	email string,
	credsJSON string,
	isActive bool,
	persistFn func(string) error,
	httpClient *http.Client,
) (*models.UsageResponse, error) {
	currentCreds := credsJSON

	oauthData, err := ExtractOAuthData(currentCreds)
	if err != nil {
		return nil, fmt.Errorf("account %d (%s): %w", accountNum, email, err)
	}

	// Refresh if not active and token expired.
	if !isActive && IsTokenExpired(oauthData.ExpiresAt) {
		updated, refreshErr := RefreshCredentials(currentCreds, httpClient)
		if refreshErr != nil {
			return nil, fmt.Errorf("account %d (%s): refresh failed: %w", accountNum, email, refreshErr)
		}
		if updated != "" {
			if persistFn != nil {
				if err := persistFn(updated); err != nil {
					return nil, fmt.Errorf("account %d (%s): persist failed: %w", accountNum, email, err)
				}
			}
			currentCreds = updated
		}
	}

	token, err := ExtractAccessToken(currentCreds)
	if err != nil {
		return nil, fmt.Errorf("account %d (%s): %w", accountNum, email, err)
	}

	usage, fetchErr := FetchUsage(token, httpClient)
	if fetchErr != nil && !isActive {
		// On error (likely 401), try refreshing once and retry.
		updated, refreshErr := RefreshCredentials(currentCreds, httpClient)
		if refreshErr != nil {
			return nil, fetchErr // Return original fetch error.
		}
		if updated != "" {
			if persistFn != nil {
				if err := persistFn(updated); err != nil {
					return nil, fmt.Errorf("account %d (%s): persist failed: %w", accountNum, email, err)
				}
			}
			newToken, tokenErr := ExtractAccessToken(updated)
			if tokenErr != nil {
				return nil, fetchErr
			}
			return FetchUsage(newToken, httpClient)
		}
		return nil, fetchErr
	}
	return usage, fetchErr
}

// UsageCache provides file-based caching for usage responses.
type UsageCache struct {
	CacheDir string
	TTL      time.Duration
}

// cachedEntry wraps a usage response with a cache timestamp.
type cachedEntry struct {
	Data     *models.UsageResponse `json:"data"`
	CachedAt int64                 `json:"cachedAt"` // unix milliseconds
}

// NewUsageCache creates a new UsageCache with the given cache directory and TTL.
func NewUsageCache(cacheDir string, ttl time.Duration) *UsageCache {
	return &UsageCache{
		CacheDir: cacheDir,
		TTL:      ttl,
	}
}

// cacheFilePath returns the path to the cache file for a given account number.
func (c *UsageCache) cacheFilePath(accountNum int) string {
	return filepath.Join(c.CacheDir, fmt.Sprintf("usage-%d.json", accountNum))
}

// GetCached reads the cache file for accountNum and returns the cached data if
// it is still valid (within TTL). Returns the data and true if valid, or nil
// and false if missing, expired, or corrupted.
func (c *UsageCache) GetCached(accountNum int) (*models.UsageResponse, bool) {
	data, err := os.ReadFile(c.cacheFilePath(accountNum))
	if err != nil {
		return nil, false
	}

	var entry cachedEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, false
	}

	nowMS := time.Now().UnixMilli()
	ttlMS := c.TTL.Milliseconds()
	if entry.CachedAt+ttlMS <= nowMS {
		return nil, false
	}

	return entry.Data, true
}

// SetCache writes a usage response to the cache file for accountNum.
func (c *UsageCache) SetCache(accountNum int, result *models.UsageResponse) error {
	if err := os.MkdirAll(c.CacheDir, 0755); err != nil {
		return fmt.Errorf("creating cache directory: %w", err)
	}

	entry := cachedEntry{
		Data:     result,
		CachedAt: time.Now().UnixMilli(),
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshaling cache entry: %w", err)
	}

	if err := os.WriteFile(c.cacheFilePath(accountNum), data, 0600); err != nil {
		return fmt.Errorf("writing cache file: %w", err)
	}
	return nil
}

// FetchUsageWithCache fetches usage for an account, using the cache if
// useCache is true and a valid cached entry exists. On cache miss or when
// useCache is false, it calls FetchUsageForAccount and updates the cache.
func FetchUsageWithCache(
	cache *UsageCache,
	useCache bool,
	accountNum int,
	email string,
	credsJSON string,
	isActive bool,
	persistFn func(string) error,
	httpClient *http.Client,
) (*models.UsageResponse, error) {
	if useCache && cache != nil {
		if data, ok := cache.GetCached(accountNum); ok {
			return data, nil
		}
	}

	usage, err := FetchUsageForAccount(accountNum, email, credsJSON, isActive, persistFn, httpClient)
	if err != nil {
		return nil, err
	}

	if cache != nil {
		// Best-effort cache update; don't fail the request if caching fails.
		_ = cache.SetCache(accountNum, usage)
	}

	return usage, nil
}

// FormatReset parses an ISO8601 reset timestamp and returns a human-readable
// countdown string and a clock string showing the local time.
// countdown examples: "2h 30m", "45m", "< 1m"
// clock example: "15:04"
// On parse error, both return "N/A".
func FormatReset(resetsAt string) (countdown string, clock string) {
	t, err := time.Parse(time.RFC3339, resetsAt)
	if err != nil {
		return "N/A", "N/A"
	}

	remaining := time.Until(t)
	if remaining <= 0 {
		return "< 1m", t.Local().Format("15:04")
	}

	totalMinutes := int(math.Floor(remaining.Minutes()))
	hours := totalMinutes / 60
	minutes := totalMinutes % 60

	if hours > 0 {
		countdown = fmt.Sprintf("%dh %dm", hours, minutes)
	} else if minutes >= 1 {
		countdown = fmt.Sprintf("%dm", minutes)
	} else {
		countdown = "< 1m"
	}

	clock = t.Local().Format("15:04")
	return countdown, clock
}
