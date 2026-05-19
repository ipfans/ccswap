package switcher_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ipfans/ccswap/pkg/models"
)

// usageByToken maps access tokens to their usage responses for the
// per-account usage mock server.
type usageByToken map[string]models.UsageResponse

// newAutoUsageTransport creates an http.RoundTripper that returns usage
// responses based on the access token in the request, using the urlRewriter
// pattern from oauth_test.go. For GET requests (usage), it looks up the
// Bearer token in the mapping. For POST requests (token refresh), it returns
// the same token back (no-op refresh).
type autoUsageTransport struct {
	usageMap usageByToken
}

func (a *autoUsageTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost {
		// Token refresh: parse the request to get the refresh token, then
		// return a response with the same access token pattern.
		// We use the convention that refresh-tok-X refreshes to tok-X.
		bodyBytes, _ := io.ReadAll(req.Body)
		var reqBody map[string]string
		json.Unmarshal(bodyBytes, &reqBody)

		refreshToken := reqBody["refresh_token"]
		// Convention: refresh token "refresh-tok-X" -> access token "tok-X"
		accessToken := strings.TrimPrefix(refreshToken, "refresh-")

		resp := map[string]any{
			"access_token": accessToken,
			"expires_in":   3600,
		}
		body, _ := json.Marshal(resp)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(body)),
		}, nil
	}

	// GET request: usage endpoint.
	auth := req.Header.Get("Authorization")
	token := strings.TrimPrefix(auth, "Bearer ")

	if usage, ok := a.usageMap[token]; ok {
		body, _ := json.Marshal(usage)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(body)),
		}, nil
	}

	// Unknown token: return 500.
	return &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":"unknown token"}`))),
	}, nil
}

func newAutoHTTPClient(usageMap usageByToken) *http.Client {
	return &http.Client{Transport: &autoUsageTransport{usageMap: usageMap}}
}

// makeUsage is a helper to build a UsageResponse from 5h and 7d utilization
// fractions (e.g., 0.60 for 60%).
func makeUsage(fiveH, sevenD float64) models.UsageResponse {
	return models.UsageResponse{
		FiveHour: &models.UsageWindow{
			Utilization: fiveH,
			ResetsAt:    "2025-06-15T10:00:00Z",
		},
		SevenDay: &models.UsageWindow{
			Utilization: sevenD,
			ResetsAt:    "2025-06-20T00:00:00Z",
		},
	}
}

func TestAutoSwitch_CurrentAccountHealthy(t *testing.T) {
	env := setupTestEnv(t)

	usageMap := usageByToken{
		"tok-alice": makeUsage(0.60, 0.40), // 60% 5h, 40% 7d — below 80% threshold
	}
	sw := newTestSwitcher(t, env, newAutoHTTPClient(usageMap))

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")
	addAccountFixture(t, sw, env, "bob@example.com", "org-2", "OrgTwo", "tok-bob")
	restoreActiveFiles(t, env)

	err := sw.AutoSwitch(80)
	if err != nil {
		t.Fatalf("AutoSwitch: unexpected error: %v", err)
	}

	// Verify no switch occurred: active account should still be #1.
	cfg := readSequenceFile(t, env)
	if cfg.ActiveAccountNumber == nil || *cfg.ActiveAccountNumber != 1 {
		t.Errorf("expected active account 1 (no switch), got %v", cfg.ActiveAccountNumber)
	}
}

func TestAutoSwitch_SkipsUnhealthyCandidateSwitchesToHealthy(t *testing.T) {
	env := setupTestEnv(t)

	usageMap := usageByToken{
		"tok-alice":   makeUsage(0.92, 0.50), // 92% 5h — over 80%
		"tok-bob":     makeUsage(0.85, 0.70), // 85% 5h — over 80%, skip
		"tok-charlie": makeUsage(0.50, 0.30), // 50% 5h, 30% 7d — healthy
	}
	sw := newTestSwitcher(t, env, newAutoHTTPClient(usageMap))

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")
	addAccountFixture(t, sw, env, "bob@example.com", "org-2", "OrgTwo", "tok-bob")
	addAccountFixture(t, sw, env, "charlie@example.com", "org-3", "OrgThree", "tok-charlie")
	restoreActiveFiles(t, env)

	err := sw.AutoSwitch(80)
	if err != nil {
		t.Fatalf("AutoSwitch: unexpected error: %v", err)
	}

	// Should have skipped bob (#2) and switched to charlie (#3).
	cfg := readSequenceFile(t, env)
	if cfg.ActiveAccountNumber == nil || *cfg.ActiveAccountNumber != 3 {
		t.Fatalf("expected active account 3 (charlie), got %v", cfg.ActiveAccountNumber)
	}
}

func TestAutoSwitch_AllAccountsOverThreshold(t *testing.T) {
	env := setupTestEnv(t)

	usageMap := usageByToken{
		"tok-alice": makeUsage(0.90, 0.50),
		"tok-bob":   makeUsage(0.85, 0.70),
	}
	sw := newTestSwitcher(t, env, newAutoHTTPClient(usageMap))

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")
	addAccountFixture(t, sw, env, "bob@example.com", "org-2", "OrgTwo", "tok-bob")
	restoreActiveFiles(t, env)

	err := sw.AutoSwitch(80)
	if err == nil {
		t.Fatal("expected error when all accounts over threshold, got nil")
	}
	if !strings.Contains(err.Error(), "no healthy account") {
		t.Errorf("expected 'no healthy account' in error, got: %v", err)
	}
}

func TestAutoSwitch_SevenDayOverThresholdTriggers(t *testing.T) {
	env := setupTestEnv(t)

	// Current account: 5h is OK (30%) but 7d is over (85%).
	usageMap := usageByToken{
		"tok-alice": makeUsage(0.30, 0.85), // 7d over 80% triggers switch
		"tok-bob":   makeUsage(0.20, 0.20), // healthy candidate
	}
	sw := newTestSwitcher(t, env, newAutoHTTPClient(usageMap))

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")
	addAccountFixture(t, sw, env, "bob@example.com", "org-2", "OrgTwo", "tok-bob")
	restoreActiveFiles(t, env)

	err := sw.AutoSwitch(80)
	if err != nil {
		t.Fatalf("AutoSwitch: unexpected error: %v", err)
	}

	cfg := readSequenceFile(t, env)
	if cfg.ActiveAccountNumber == nil || *cfg.ActiveAccountNumber != 2 {
		t.Fatalf("expected active account 2 (bob) after 7d-triggered switch, got %v", cfg.ActiveAccountNumber)
	}
}

func TestAutoSwitch_SingleAccountOverThreshold(t *testing.T) {
	env := setupTestEnv(t)

	usageMap := usageByToken{
		"tok-alice": makeUsage(0.90, 0.50),
	}
	sw := newTestSwitcher(t, env, newAutoHTTPClient(usageMap))

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")

	err := sw.AutoSwitch(80)
	if err == nil {
		t.Fatal("expected error with single account over threshold, got nil")
	}
	if !strings.Contains(err.Error(), "no healthy account") {
		t.Errorf("expected 'no healthy account' in error, got: %v", err)
	}
}

func TestAutoSwitch_Threshold100_AllHealthy(t *testing.T) {
	env := setupTestEnv(t)

	// Even 99% is below 100% threshold.
	usageMap := usageByToken{
		"tok-alice": makeUsage(0.99, 0.99),
	}
	sw := newTestSwitcher(t, env, newAutoHTTPClient(usageMap))

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")

	err := sw.AutoSwitch(100)
	if err != nil {
		t.Fatalf("AutoSwitch with threshold=100: unexpected error: %v", err)
	}

	// Should stay on account 1 (healthy at any utilization below 100%).
	cfg := readSequenceFile(t, env)
	if cfg.ActiveAccountNumber == nil || *cfg.ActiveAccountNumber != 1 {
		t.Errorf("expected active account 1, got %v", cfg.ActiveAccountNumber)
	}
}

func TestAutoSwitch_UsageAPIFailsForCurrent(t *testing.T) {
	env := setupTestEnv(t)

	// No entries in usageMap means the token lookup returns 500.
	usageMap := usageByToken{}
	sw := newTestSwitcher(t, env, newAutoHTTPClient(usageMap))

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")

	err := sw.AutoSwitch(80)
	if err == nil {
		t.Fatal("expected error when usage API fails for current account, got nil")
	}
	t.Logf("Expected error: %v", err)
}

func TestAutoSwitch_UsageAPIFailsForCandidateSkipsToNext(t *testing.T) {
	env := setupTestEnv(t)

	// Alice is over threshold, bob's token is missing from usageMap (API fails),
	// charlie is healthy.
	usageMap := usageByToken{
		"tok-alice":   makeUsage(0.90, 0.50),
		"tok-charlie": makeUsage(0.30, 0.20),
		// "tok-bob" is absent — API will return 500
	}
	sw := newTestSwitcher(t, env, newAutoHTTPClient(usageMap))

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")
	addAccountFixture(t, sw, env, "bob@example.com", "org-2", "OrgTwo", "tok-bob")
	addAccountFixture(t, sw, env, "charlie@example.com", "org-3", "OrgThree", "tok-charlie")
	restoreActiveFiles(t, env)

	err := sw.AutoSwitch(80)
	if err != nil {
		t.Fatalf("AutoSwitch: unexpected error: %v", err)
	}

	// Should skip bob (API failure) and switch to charlie.
	cfg := readSequenceFile(t, env)
	if cfg.ActiveAccountNumber == nil || *cfg.ActiveAccountNumber != 3 {
		t.Fatalf("expected active account 3 (charlie), got %v", cfg.ActiveAccountNumber)
	}
}

func TestAutoSwitch_FullIntegration(t *testing.T) {
	env := setupTestEnv(t)

	// Use httptest server for full round-trip testing.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodPost {
			// Token refresh: return the same token.
			json.NewEncoder(w).Encode(map[string]any{
				"access_token": "tok-bob",
				"expires_in":   3600,
			})
			return
		}

		// Usage endpoint: differentiate by bearer token.
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")

		switch token {
		case "tok-alice":
			json.NewEncoder(w).Encode(makeUsage(0.95, 0.60))
		case "tok-bob":
			json.NewEncoder(w).Encode(makeUsage(0.40, 0.30))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer ts.Close()

	client := ts.Client()
	client.Transport = &urlRewriter{target: ts.URL, transport: http.DefaultTransport}

	sw := newTestSwitcher(t, env, client)

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")
	addAccountFixture(t, sw, env, "bob@example.com", "org-2", "OrgTwo", "tok-bob")
	restoreActiveFiles(t, env)

	// Verify precondition: alice is active.
	cfg := readSequenceFile(t, env)
	if cfg.ActiveAccountNumber == nil || *cfg.ActiveAccountNumber != 1 {
		t.Fatalf("precondition: expected active account 1, got %v", cfg.ActiveAccountNumber)
	}

	// Run auto-switch.
	err := sw.AutoSwitch(80)
	if err != nil {
		t.Fatalf("AutoSwitch: %v", err)
	}

	// Verify bob is now active.
	cfg = readSequenceFile(t, env)
	if cfg.ActiveAccountNumber == nil || *cfg.ActiveAccountNumber != 2 {
		t.Fatalf("expected active account 2 (bob) after auto-switch, got %v", cfg.ActiveAccountNumber)
	}
}

func TestAutoSwitch_NoActiveAccount(t *testing.T) {
	env := setupTestEnv(t)

	usageMap := usageByToken{}
	sw := newTestSwitcher(t, env, newAutoHTTPClient(usageMap))

	// Don't add any accounts — no active account set.
	err := sw.AutoSwitch(80)
	if err == nil {
		t.Fatal("expected error with no active account, got nil")
	}
	if !strings.Contains(err.Error(), "no active account") {
		t.Errorf("expected 'no active account' in error, got: %v", err)
	}
}

// urlRewriter is the same pattern used in oauth_test.go to redirect requests
// to a test server URL while keeping the path.
type urlRewriter struct {
	target    string
	transport http.RoundTripper
}

func (u *urlRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	newURL := u.target + req.URL.Path
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, newURL, req.Body)
	if err != nil {
		return nil, err
	}
	newReq.Header = req.Header
	return u.transport.RoundTrip(newReq)
}
