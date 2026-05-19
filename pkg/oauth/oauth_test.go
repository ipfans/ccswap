package oauth_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ipfans/ccswap/pkg/models"
	"github.com/ipfans/ccswap/pkg/oauth"
)

// validCredsJSON returns a credential JSON string with realistic token data.
func validCredsJSON(accessToken, refreshToken string, expiresAt int64) string {
	return fmt.Sprintf(`{
		"claudeAiOauth": {
			"accessToken": %q,
			"refreshToken": %q,
			"expiresAt": %d,
			"scopes": ["user:inference","user:profile"]
		}
	}`, accessToken, refreshToken, expiresAt)
}

func TestExtractAccessToken_Valid(t *testing.T) {
	creds := validCredsJSON("test-access-token", "test-refresh-token", time.Now().Add(time.Hour).UnixMilli())
	token, err := oauth.ExtractAccessToken(creds)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "test-access-token" {
		t.Errorf("token = %q, want %q", token, "test-access-token")
	}
}

func TestExtractAccessToken_MissingOAuth(t *testing.T) {
	creds := `{"someOtherField": "value"}`
	_, err := oauth.ExtractAccessToken(creds)
	if err == nil {
		t.Fatal("expected error for missing claudeAiOauth, got nil")
	}
	if !strings.Contains(err.Error(), "claudeAiOauth") {
		t.Errorf("error message should mention claudeAiOauth: %v", err)
	}
}

func TestIsTokenExpired_Expired(t *testing.T) {
	// Token that expired 10 minutes ago.
	expiresAt := time.Now().Add(-10 * time.Minute).UnixMilli()
	if !oauth.IsTokenExpired(expiresAt) {
		t.Error("expected expired token to return true")
	}
}

func TestIsTokenExpired_Fresh(t *testing.T) {
	// Token that expires in 1 hour (well beyond the 5-minute buffer).
	expiresAt := time.Now().Add(time.Hour).UnixMilli()
	if oauth.IsTokenExpired(expiresAt) {
		t.Error("expected fresh token to return false")
	}
}

func TestFormatReset_ValidFuture(t *testing.T) {
	// 2 hours and 30 minutes from now.
	resetTime := time.Now().Add(2*time.Hour + 30*time.Minute + 30*time.Second)
	resetStr := resetTime.UTC().Format(time.RFC3339)

	countdown, clock := oauth.FormatReset(resetStr)

	if !strings.Contains(countdown, "h") || !strings.Contains(countdown, "m") {
		t.Errorf("countdown = %q, expected format like '2h 30m'", countdown)
	}

	expectedClock := resetTime.Local().Format("15:04")
	if clock != expectedClock {
		t.Errorf("clock = %q, want %q", clock, expectedClock)
	}
}

func TestFormatReset_InvalidString(t *testing.T) {
	countdown, clock := oauth.FormatReset("not-a-date")
	if countdown != "N/A" {
		t.Errorf("countdown = %q, want %q", countdown, "N/A")
	}
	if clock != "N/A" {
		t.Errorf("clock = %q, want %q", clock, "N/A")
	}
}

func TestFormatReset_PastTime(t *testing.T) {
	pastTime := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	countdown, _ := oauth.FormatReset(pastTime)
	if countdown != "< 1m" {
		t.Errorf("countdown for past time = %q, want %q", countdown, "< 1m")
	}
}

func TestRefreshCredentials_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		ct := r.Header.Get("Content-Type")
		if ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access-token",
			"expires_in":    3600,
			"refresh_token": "new-refresh-token",
			"scopes":        []string{"user:inference"},
		})
	}))
	defer ts.Close()

	creds := validCredsJSON("old-token", "old-refresh", time.Now().Add(-time.Hour).UnixMilli())

	// Override TokenURL by pointing httpClient at test server via transport.
	client := ts.Client()

	// We need to rewrite the URL since RefreshCredentials uses the const TokenURL.
	// Use a custom RoundTripper that redirects to the test server.
	client.Transport = &urlRewriter{
		target:    ts.URL,
		transport: http.DefaultTransport,
	}

	updated, err := oauth.RefreshCredentials(creds, client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated == "" {
		t.Fatal("expected updated creds, got empty string")
	}

	// Verify the updated JSON contains the new token.
	token, err := oauth.ExtractAccessToken(updated)
	if err != nil {
		t.Fatalf("extracting token from updated creds: %v", err)
	}
	if token != "new-access-token" {
		t.Errorf("token = %q, want %q", token, "new-access-token")
	}

	// Verify refresh token was updated.
	oauthData, err := oauth.ExtractOAuthData(updated)
	if err != nil {
		t.Fatalf("extracting oauth data: %v", err)
	}
	if oauthData.RefreshToken != "new-refresh-token" {
		t.Errorf("refreshToken = %q, want %q", oauthData.RefreshToken, "new-refresh-token")
	}
}

func TestRefreshCredentials_HTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	creds := validCredsJSON("old-token", "old-refresh", time.Now().Add(-time.Hour).UnixMilli())
	client := ts.Client()
	client.Transport = &urlRewriter{target: ts.URL, transport: http.DefaultTransport}

	result, err := oauth.RefreshCredentials(creds, client)
	if err != nil {
		t.Fatalf("expected nil error on HTTP error, got: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string on HTTP error, got: %q", result)
	}
}

func TestFetchUsage_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token" {
			t.Errorf("Authorization = %q, want %q", auth, "Bearer test-token")
		}
		beta := r.Header.Get("anthropic-beta")
		if beta != oauth.BetaHeader {
			t.Errorf("anthropic-beta = %q, want %q", beta, oauth.BetaHeader)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(models.UsageResponse{
			FiveHour: &models.UsageWindow{
				Utilization: 0.42,
				ResetsAt:    "2025-06-15T10:00:00Z",
			},
			SevenDay: &models.UsageWindow{
				Utilization: 0.75,
				ResetsAt:    "2025-06-20T00:00:00Z",
			},
		})
	}))
	defer ts.Close()

	client := ts.Client()
	client.Transport = &urlRewriter{target: ts.URL, transport: http.DefaultTransport}

	usage, err := oauth.FetchUsage("test-token", client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage.FiveHour == nil {
		t.Fatal("FiveHour is nil")
	}
	if usage.FiveHour.Utilization != 0.42 {
		t.Errorf("FiveHour.Utilization = %f, want 0.42", usage.FiveHour.Utilization)
	}
	if usage.SevenDay == nil {
		t.Fatal("SevenDay is nil")
	}
	if usage.SevenDay.Utilization != 0.75 {
		t.Errorf("SevenDay.Utilization = %f, want 0.75", usage.SevenDay.Utilization)
	}
}

func TestFetchUsageForAccount_ExpiredToken_Refreshes(t *testing.T) {
	// Handler that serves both token refresh and usage endpoints.
	refreshCalled := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodPost {
			// Token refresh endpoint.
			refreshCalled = true
			json.NewEncoder(w).Encode(map[string]any{
				"access_token": "refreshed-token",
				"expires_in":   3600,
			})
			return
		}
		// Usage endpoint.
		json.NewEncoder(w).Encode(models.UsageResponse{
			FiveHour: &models.UsageWindow{
				Utilization: 0.5,
				ResetsAt:    "2025-06-15T10:00:00Z",
			},
		})
	}))
	defer ts.Close()

	// Token expired 10 minutes ago.
	creds := validCredsJSON("expired-token", "my-refresh-token", time.Now().Add(-10*time.Minute).UnixMilli())

	client := ts.Client()
	client.Transport = &urlRewriter{target: ts.URL, transport: http.DefaultTransport}

	var persistedCreds string
	persistFn := func(updated string) error {
		persistedCreds = updated
		return nil
	}

	usage, err := oauth.FetchUsageForAccount(1, "test@example.com", creds, false, persistFn, client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !refreshCalled {
		t.Error("expected refresh to be called for expired token")
	}
	if persistedCreds == "" {
		t.Error("expected persistFn to be called with updated creds")
	}
	if usage == nil || usage.FiveHour == nil {
		t.Fatal("expected non-nil usage with FiveHour")
	}
	if usage.FiveHour.Utilization != 0.5 {
		t.Errorf("Utilization = %f, want 0.5", usage.FiveHour.Utilization)
	}
}

func TestFetchUsageForAccount_401_RefreshesAndRetries(t *testing.T) {
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodPost {
			// Token refresh endpoint.
			json.NewEncoder(w).Encode(map[string]any{
				"access_token": "new-valid-token",
				"expires_in":   3600,
			})
			return
		}

		// Usage endpoint: first call returns 401, second succeeds.
		callCount++
		if callCount == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		json.NewEncoder(w).Encode(models.UsageResponse{
			FiveHour: &models.UsageWindow{
				Utilization: 0.33,
				ResetsAt:    "2025-06-15T12:00:00Z",
			},
		})
	}))
	defer ts.Close()

	// Token is not expired (so the pre-check won't trigger refresh).
	creds := validCredsJSON("stale-token", "my-refresh-token", time.Now().Add(time.Hour).UnixMilli())

	client := ts.Client()
	client.Transport = &urlRewriter{target: ts.URL, transport: http.DefaultTransport}

	persistFn := func(updated string) error { return nil }

	usage, err := oauth.FetchUsageForAccount(1, "test@example.com", creds, false, persistFn, client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount < 2 {
		t.Errorf("expected at least 2 usage API calls (first 401, second success), got %d", callCount)
	}
	if usage == nil || usage.FiveHour == nil {
		t.Fatal("expected non-nil usage with FiveHour")
	}
	if usage.FiveHour.Utilization != 0.33 {
		t.Errorf("Utilization = %f, want 0.33", usage.FiveHour.Utilization)
	}
}

func TestUsageCacheGetCached_ValidEntry(t *testing.T) {
	dir := t.TempDir()
	cache := oauth.NewUsageCache(dir, 10*time.Minute)

	usage := &models.UsageResponse{
		FiveHour: &models.UsageWindow{
			Utilization: 0.6,
			ResetsAt:    "2025-06-15T10:00:00Z",
		},
	}

	if err := cache.SetCache(1, usage); err != nil {
		t.Fatalf("SetCache: %v", err)
	}

	got, ok := cache.GetCached(1)
	if !ok {
		t.Fatal("expected cached entry to be valid")
	}
	if got.FiveHour == nil {
		t.Fatal("cached FiveHour is nil")
	}
	if got.FiveHour.Utilization != 0.6 {
		t.Errorf("cached Utilization = %f, want 0.6", got.FiveHour.Utilization)
	}
}

func TestUsageCacheGetCached_ExpiredEntry(t *testing.T) {
	dir := t.TempDir()
	// TTL of 0 means everything is expired immediately.
	cache := oauth.NewUsageCache(dir, 0)

	usage := &models.UsageResponse{
		FiveHour: &models.UsageWindow{
			Utilization: 0.6,
			ResetsAt:    "2025-06-15T10:00:00Z",
		},
	}

	if err := cache.SetCache(1, usage); err != nil {
		t.Fatalf("SetCache: %v", err)
	}

	// With TTL=0, cachedAt + 0 <= now is always true, so it's expired.
	_, ok := cache.GetCached(1)
	if ok {
		t.Error("expected expired cache entry, got valid")
	}
}

func TestUsageCacheGetCached_CorruptedFile(t *testing.T) {
	dir := t.TempDir()
	cache := oauth.NewUsageCache(dir, 10*time.Minute)

	// Write corrupted cache file.
	cachePath := filepath.Join(dir, "usage-1.json")
	if err := os.WriteFile(cachePath, []byte("{corrupted json!!!"), 0600); err != nil {
		t.Fatalf("writing corrupted cache: %v", err)
	}

	_, ok := cache.GetCached(1)
	if ok {
		t.Error("expected corrupted cache to return false")
	}
}

func TestFetchUsageWithCache_NoCacheAlwaysQueries(t *testing.T) {
	queryCalled := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			queryCalled = true
			json.NewEncoder(w).Encode(models.UsageResponse{
				FiveHour: &models.UsageWindow{
					Utilization: 0.9,
					ResetsAt:    "2025-06-15T10:00:00Z",
				},
			})
		}
	}))
	defer ts.Close()

	creds := validCredsJSON("test-token", "test-refresh", time.Now().Add(time.Hour).UnixMilli())
	client := ts.Client()
	client.Transport = &urlRewriter{target: ts.URL, transport: http.DefaultTransport}

	dir := t.TempDir()
	cache := oauth.NewUsageCache(dir, 10*time.Minute)

	// Pre-populate cache.
	_ = cache.SetCache(1, &models.UsageResponse{
		FiveHour: &models.UsageWindow{
			Utilization: 0.1,
			ResetsAt:    "2025-06-15T10:00:00Z",
		},
	})

	// useCache=false should ignore the cache.
	usage, err := oauth.FetchUsageWithCache(cache, false, 1, "test@example.com", creds, true, nil, client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !queryCalled {
		t.Error("expected HTTP query to be made when useCache=false")
	}
	if usage.FiveHour.Utilization != 0.9 {
		t.Errorf("Utilization = %f, want 0.9 (from server, not cache)", usage.FiveHour.Utilization)
	}
}

// urlRewriter is a custom RoundTripper that redirects requests to a test server
// URL. This allows tests to use the const TokenURL/UsageURL in the production
// code while pointing requests at httptest servers.
type urlRewriter struct {
	target    string
	transport http.RoundTripper
}

func (u *urlRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	// Replace the scheme+host with the test server URL, keeping the path.
	newURL := u.target + req.URL.Path
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, newURL, req.Body)
	if err != nil {
		return nil, err
	}
	newReq.Header = req.Header
	return u.transport.RoundTrip(newReq)
}
