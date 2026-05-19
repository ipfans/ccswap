package switcher_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ipfans/ccswap/pkg/config"
	"github.com/ipfans/ccswap/pkg/credential"
	"github.com/ipfans/ccswap/pkg/models"
	"github.com/ipfans/ccswap/pkg/switcher"
)

// testEnv holds all paths for a self-contained test environment.
type testEnv struct {
	backupRoot      string
	claudeConfigPath string
	fakeHome        string
	credsPath       string
}

// setupTestEnv creates a fully isolated test environment using t.TempDir().
// It sets HOME and CLAUDE_CONFIG_DIR so that credential.ReadActiveCredentials
// and config.GetClaudeConfigPath resolve to temp paths.
func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	fakeHome := t.TempDir()
	backupRoot := filepath.Join(fakeHome, ".claude-swap")
	claudeDir := filepath.Join(fakeHome, ".claude-config")
	claudeConfigPath := filepath.Join(claudeDir, "claude.json")

	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		t.Fatalf("creating claude config dir: %v", err)
	}

	// Create ~/.claude/ directory for active credentials.
	credsDir := filepath.Join(fakeHome, ".claude")
	if err := os.MkdirAll(credsDir, 0700); err != nil {
		t.Fatalf("creating .claude dir: %v", err)
	}

	t.Setenv("HOME", fakeHome)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	return &testEnv{
		backupRoot:       backupRoot,
		claudeConfigPath: claudeConfigPath,
		fakeHome:         fakeHome,
		credsPath:        filepath.Join(credsDir, ".credentials.json"),
	}
}

// makeCredsJSON builds a minimal credentials JSON string.
func makeCredsJSON(accessToken, refreshToken string) string {
	creds := map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":  accessToken,
			"refreshToken": refreshToken,
			"expiresAt":    float64(9999999999999),
			"scopes":       []string{"user:inference"},
		},
	}
	b, _ := json.Marshal(creds)
	return string(b)
}

// makeClaudeConfig builds a minimal claude.json map and writes it to path.
func makeClaudeConfig(t *testing.T, path string, email, orgUUID, orgName string) {
	t.Helper()
	cfg := map[string]any{
		"oauthAccount": map[string]any{
			"emailAddress":     email,
			"accountUuid":      "uuid-" + email,
			"organizationUuid": orgUUID,
			"organizationName": orgName,
			"organizationRole": "member",
			"displayName":      email,
		},
		"someSetting": "preserved",
	}
	if err := config.WriteClaudeConfig(path, cfg); err != nil {
		t.Fatalf("writing claude config fixture: %v", err)
	}
}

// writeActiveCreds writes credentials JSON to the active credentials file.
func writeActiveCreds(t *testing.T, credsPath, credsJSON string) {
	t.Helper()
	if err := os.WriteFile(credsPath, []byte(credsJSON), 0600); err != nil {
		t.Fatalf("writing active creds fixture: %v", err)
	}
}

// fakeUsageTransport is an http.RoundTripper that returns a fixed usage
// response for any request, avoiding real network calls in tests.
type fakeUsageTransport struct{}

func (f *fakeUsageTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp := models.UsageResponse{
		FiveHour: &models.UsageWindow{Utilization: 0.25, ResetsAt: "2025-06-01T12:00:00Z"},
		SevenDay: &models.UsageWindow{Utilization: 0.50, ResetsAt: "2025-06-07T00:00:00Z"},
	}
	body, _ := json.Marshal(resp)

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}, nil
}

// fakeHTTPClient returns an *http.Client that intercepts all requests and
// returns a fixed usage response, so no real network calls are made.
func fakeHTTPClient() *http.Client {
	return &http.Client{Transport: &fakeUsageTransport{}}
}

// newTestSwitcher creates a Switcher configured with test paths and HTTP client.
func newTestSwitcher(t *testing.T, env *testEnv, httpClient *http.Client) *switcher.Switcher {
	t.Helper()
	sw, err := switcher.NewSwitcherWithPaths(env.backupRoot, env.claudeConfigPath, httpClient)
	if err != nil {
		t.Fatalf("NewSwitcherWithPaths: %v", err)
	}
	return sw
}

// readSequenceFile reads and parses the sequence.json from the test environment.
func readSequenceFile(t *testing.T, env *testEnv) *models.SwapConfig {
	t.Helper()
	seqPath := filepath.Join(env.backupRoot, "sequence.json")
	cfg, err := config.ReadSwapConfig(seqPath)
	if err != nil {
		t.Fatalf("reading sequence.json: %v", err)
	}
	return cfg
}

// addAccountFixture sets up claude.json + creds, then calls AddAccount.
func addAccountFixture(t *testing.T, sw *switcher.Switcher, env *testEnv, email, orgUUID, orgName, token string) string {
	t.Helper()
	credsJSON := makeCredsJSON(token, "refresh-"+token)
	makeClaudeConfig(t, env.claudeConfigPath, email, orgUUID, orgName)
	writeActiveCreds(t, env.credsPath, credsJSON)
	result, err := sw.AddAccount()
	if err != nil {
		t.Fatalf("AddAccount(%s): %v", email, err)
	}
	return result
}

// restoreActiveFiles restores the active credentials and claude.json from
// the backup of the currently active account. Call this after adding multiple
// accounts to ensure the active files on disk match the active account in
// sequence.json (since each addAccountFixture overwrites the active files).
func restoreActiveFiles(t *testing.T, env *testEnv) {
	t.Helper()
	seqPath := filepath.Join(env.backupRoot, "sequence.json")
	cfg, err := config.ReadSwapConfig(seqPath)
	if err != nil {
		t.Fatalf("restoreActiveFiles: reading sequence: %v", err)
	}
	if cfg.ActiveAccountNumber == nil {
		return
	}
	num := *cfg.ActiveAccountNumber
	key := strconv.Itoa(num)
	acct, ok := cfg.Accounts[key]
	if !ok {
		t.Fatalf("restoreActiveFiles: active account #%d not in accounts", num)
	}

	store, err := credential.NewStore(env.backupRoot)
	if err != nil {
		t.Fatalf("restoreActiveFiles: NewStore: %v", err)
	}

	creds, err := store.ReadAccountCredentials(num, acct.Email)
	if err != nil {
		t.Fatalf("restoreActiveFiles: reading creds: %v", err)
	}
	if creds != "" {
		writeActiveCreds(t, env.credsPath, creds)
	}

	cfgJSON, err := store.ReadAccountConfig(num, acct.Email)
	if err != nil {
		t.Fatalf("restoreActiveFiles: reading config: %v", err)
	}
	if cfgJSON != "" {
		if err := os.WriteFile(env.claudeConfigPath, []byte(cfgJSON), 0600); err != nil {
			t.Fatalf("restoreActiveFiles: writing claude config: %v", err)
		}
	}
}

func TestAddAccount(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	result := addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")

	if result == "" {
		t.Fatal("AddAccount returned empty result")
	}
	t.Logf("AddAccount result: %s", result)

	cfg := readSequenceFile(t, env)

	if len(cfg.Sequence) != 1 {
		t.Fatalf("expected 1 account in sequence, got %d", len(cfg.Sequence))
	}
	if cfg.Sequence[0] != 1 {
		t.Errorf("expected account number 1, got %d", cfg.Sequence[0])
	}

	acct, ok := cfg.Accounts["1"]
	if !ok {
		t.Fatal("account 1 not found in Accounts map")
	}
	if acct.Email != "alice@example.com" {
		t.Errorf("email = %q, want alice@example.com", acct.Email)
	}
	if acct.OrganizationName != "OrgOne" {
		t.Errorf("orgName = %q, want OrgOne", acct.OrganizationName)
	}

	// Verify credentials were backed up.
	store, _ := credential.NewStore(env.backupRoot)
	creds, err := store.ReadAccountCredentials(1, "alice@example.com")
	if err != nil {
		t.Fatalf("ReadAccountCredentials: %v", err)
	}
	if creds == "" {
		t.Error("expected backed up credentials, got empty")
	}

	// First account should be set as active.
	if cfg.ActiveAccountNumber == nil {
		t.Fatal("expected ActiveAccountNumber to be set for first account")
	}
	if *cfg.ActiveAccountNumber != 1 {
		t.Errorf("ActiveAccountNumber = %d, want 1", *cfg.ActiveAccountNumber)
	}
}

func TestAddAccountExistingUpdate(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	// Add alice.
	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice-v1")

	// Add alice again with different token (same email + orgUUID).
	result := addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice-v2")

	if result == "" {
		t.Fatal("second AddAccount returned empty result")
	}
	t.Logf("Update result: %s", result)

	cfg := readSequenceFile(t, env)

	// Should still be only 1 account — not duplicated.
	if len(cfg.Sequence) != 1 {
		t.Fatalf("expected 1 account in sequence after update, got %d", len(cfg.Sequence))
	}

	// Verify credentials were updated.
	store, _ := credential.NewStore(env.backupRoot)
	creds, err := store.ReadAccountCredentials(1, "alice@example.com")
	if err != nil {
		t.Fatalf("ReadAccountCredentials: %v", err)
	}
	if creds == "" {
		t.Error("expected updated credentials, got empty")
	}

	// Parse and check the new token was written.
	var credsMap map[string]any
	json.Unmarshal([]byte(creds), &credsMap)
	oauthMap, _ := credsMap["claudeAiOauth"].(map[string]any)
	if oauthMap["accessToken"] != "tok-alice-v2" {
		t.Errorf("expected updated token tok-alice-v2, got %v", oauthMap["accessToken"])
	}
}

func TestRemoveAccount(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")
	addAccountFixture(t, sw, env, "bob@example.com", "org-2", "OrgTwo", "tok-bob")
	restoreActiveFiles(t, env)

	result, err := sw.RemoveAccount("1")
	if err != nil {
		t.Fatalf("RemoveAccount: %v", err)
	}
	t.Logf("Remove result: %s", result)

	cfg := readSequenceFile(t, env)

	if len(cfg.Sequence) != 1 {
		t.Fatalf("expected 1 account after removal, got %d", len(cfg.Sequence))
	}
	if _, ok := cfg.Accounts["1"]; ok {
		t.Error("account 1 should have been removed from Accounts map")
	}
	if _, ok := cfg.Accounts["2"]; !ok {
		t.Error("account 2 should still exist")
	}

	// Verify backup files were deleted.
	store, _ := credential.NewStore(env.backupRoot)
	creds, err := store.ReadAccountCredentials(1, "alice@example.com")
	if err != nil {
		t.Fatalf("ReadAccountCredentials: %v", err)
	}
	if creds != "" {
		t.Error("expected empty credentials after removal")
	}
}

func TestRemoveActiveAccount(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")

	cfg := readSequenceFile(t, env)
	if cfg.ActiveAccountNumber == nil || *cfg.ActiveAccountNumber != 1 {
		t.Fatal("precondition: account 1 should be active")
	}

	_, err := sw.RemoveAccount("1")
	if err != nil {
		t.Fatalf("RemoveAccount: %v", err)
	}

	cfg = readSequenceFile(t, env)
	if cfg.ActiveAccountNumber != nil {
		t.Errorf("ActiveAccountNumber should be nil after removing active account, got %d", *cfg.ActiveAccountNumber)
	}
}

func TestSwitchToByNumber(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")
	addAccountFixture(t, sw, env, "bob@example.com", "org-2", "OrgTwo", "tok-bob")
	restoreActiveFiles(t, env)

	if err := sw.SwitchTo("2"); err != nil {
		t.Fatalf("SwitchTo(2): %v", err)
	}

	cfg := readSequenceFile(t, env)
	if cfg.ActiveAccountNumber == nil || *cfg.ActiveAccountNumber != 2 {
		t.Fatalf("expected active account 2, got %v", cfg.ActiveAccountNumber)
	}

	// Verify active credentials are bob's.
	activeCreds, err := credential.ReadActiveCredentials()
	if err != nil {
		t.Fatalf("ReadActiveCredentials: %v", err)
	}

	var credsMap map[string]any
	json.Unmarshal([]byte(activeCreds), &credsMap)
	oauthMap, _ := credsMap["claudeAiOauth"].(map[string]any)
	if oauthMap["accessToken"] != "tok-bob" {
		t.Errorf("active token = %v, want tok-bob", oauthMap["accessToken"])
	}

	// Verify claude.json was updated with bob's oauthAccount.
	claudeCfg, err := config.ReadClaudeConfig(env.claudeConfigPath)
	if err != nil {
		t.Fatalf("ReadClaudeConfig: %v", err)
	}
	oaRaw, ok := claudeCfg["oauthAccount"]
	if !ok {
		t.Fatal("claude.json missing oauthAccount after switch")
	}
	oaBytes, _ := json.Marshal(oaRaw)
	var oa models.OAuthAccount
	json.Unmarshal(oaBytes, &oa)
	if oa.EmailAddress != "bob@example.com" {
		t.Errorf("claude.json oauthAccount email = %q, want bob@example.com", oa.EmailAddress)
	}
}

func TestSwitchToByEmailUnique(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")
	addAccountFixture(t, sw, env, "bob@example.com", "org-2", "OrgTwo", "tok-bob")
	restoreActiveFiles(t, env)

	if err := sw.SwitchTo("bob@example.com"); err != nil {
		t.Fatalf("SwitchTo(bob@example.com): %v", err)
	}

	cfg := readSequenceFile(t, env)
	if cfg.ActiveAccountNumber == nil || *cfg.ActiveAccountNumber != 2 {
		t.Fatalf("expected active account 2, got %v", cfg.ActiveAccountNumber)
	}
}

func TestSwitchToByEmailMultipleMatch(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	// Add same email with different orgs.
	addAccountFixture(t, sw, env, "shared@example.com", "org-1", "OrgOne", "tok-shared-1")
	addAccountFixture(t, sw, env, "shared@example.com", "org-2", "OrgTwo", "tok-shared-2")
	restoreActiveFiles(t, env)

	err := sw.SwitchTo("shared@example.com")
	if err == nil {
		t.Fatal("expected error for ambiguous email, got nil")
	}
	t.Logf("Expected error: %v", err)
}

func TestSwitchRotatesNext(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")
	addAccountFixture(t, sw, env, "bob@example.com", "org-2", "OrgTwo", "tok-bob")
	restoreActiveFiles(t, env)

	// Active should be 1 (alice).
	cfg := readSequenceFile(t, env)
	if cfg.ActiveAccountNumber == nil || *cfg.ActiveAccountNumber != 1 {
		t.Fatalf("precondition: expected active 1, got %v", cfg.ActiveAccountNumber)
	}

	// Switch should rotate to 2 (bob).
	if err := sw.Switch(); err != nil {
		t.Fatalf("Switch: %v", err)
	}

	cfg = readSequenceFile(t, env)
	if cfg.ActiveAccountNumber == nil || *cfg.ActiveAccountNumber != 2 {
		t.Fatalf("expected active 2 after switch, got %v", cfg.ActiveAccountNumber)
	}
}

func TestSwitchOneAccountError(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")

	err := sw.Switch()
	if err == nil {
		t.Fatal("expected error with only one account, got nil")
	}
	t.Logf("Expected error: %v", err)
}

func TestSwitchWrapsAround(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")
	addAccountFixture(t, sw, env, "bob@example.com", "org-2", "OrgTwo", "tok-bob")
	addAccountFixture(t, sw, env, "charlie@example.com", "org-3", "OrgThree", "tok-charlie")
	restoreActiveFiles(t, env)

	// Active starts at 1, switch to 2.
	if err := sw.Switch(); err != nil {
		t.Fatalf("Switch 1->2: %v", err)
	}
	cfg := readSequenceFile(t, env)
	if *cfg.ActiveAccountNumber != 2 {
		t.Fatalf("expected active 2, got %d", *cfg.ActiveAccountNumber)
	}

	// Switch to 3.
	if err := sw.Switch(); err != nil {
		t.Fatalf("Switch 2->3: %v", err)
	}
	cfg = readSequenceFile(t, env)
	if *cfg.ActiveAccountNumber != 3 {
		t.Fatalf("expected active 3, got %d", *cfg.ActiveAccountNumber)
	}

	// Switch wraps back to 1.
	if err := sw.Switch(); err != nil {
		t.Fatalf("Switch 3->1: %v", err)
	}
	cfg = readSequenceFile(t, env)
	if *cfg.ActiveAccountNumber != 1 {
		t.Fatalf("expected active 1 (wrap), got %d", *cfg.ActiveAccountNumber)
	}
}

func TestListAccounts(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")
	addAccountFixture(t, sw, env, "bob@example.com", "org-2", "OrgTwo", "tok-bob")
	restoreActiveFiles(t, env)

	result, err := sw.ListAccounts()
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	t.Logf("ListAccounts output:\n%s", result)

	if result == "" {
		t.Fatal("ListAccounts returned empty")
	}
}

func TestListAccountsEmpty(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	result, err := sw.ListAccounts()
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if result == "" {
		t.Fatal("ListAccounts returned empty for no-accounts case")
	}
	t.Logf("Empty list result: %s", result)
}

func TestStatus(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")

	result, err := sw.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	t.Logf("Status output:\n%s", result)

	if result == "" {
		t.Fatal("Status returned empty")
	}
}

func TestStatusNoActiveAccount(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	result, err := sw.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if result != "No active account" {
		t.Errorf("expected 'No active account', got %q", result)
	}
}

func TestSwitchToAlreadyActive(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")

	// SwitchTo the already-active account should be a no-op.
	if err := sw.SwitchTo("1"); err != nil {
		t.Fatalf("SwitchTo already active: %v", err)
	}
}

func TestSwitchToNonexistentAccount(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")

	err := sw.SwitchTo("99")
	if err == nil {
		t.Fatal("expected error for nonexistent account, got nil")
	}
	t.Logf("Expected error: %v", err)
}

func TestSwitchToNonexistentEmail(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")

	err := sw.SwitchTo("nobody@example.com")
	if err == nil {
		t.Fatal("expected error for nonexistent email, got nil")
	}
	t.Logf("Expected error: %v", err)
}

func TestPerformSwitchPreservesOtherClaudeSettings(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")
	addAccountFixture(t, sw, env, "bob@example.com", "org-2", "OrgTwo", "tok-bob")
	restoreActiveFiles(t, env)

	// After switching, verify "someSetting" is preserved in claude.json.
	if err := sw.SwitchTo("2"); err != nil {
		t.Fatalf("SwitchTo: %v", err)
	}

	claudeCfg, err := config.ReadClaudeConfig(env.claudeConfigPath)
	if err != nil {
		t.Fatalf("ReadClaudeConfig: %v", err)
	}

	if val, ok := claudeCfg["someSetting"]; !ok || val != "preserved" {
		t.Errorf("expected someSetting=preserved, got %v", val)
	}
}

func TestRemoveAccountByEmail(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")
	addAccountFixture(t, sw, env, "bob@example.com", "org-2", "OrgTwo", "tok-bob")
	restoreActiveFiles(t, env)

	result, err := sw.RemoveAccount("bob@example.com")
	if err != nil {
		t.Fatalf("RemoveAccount by email: %v", err)
	}
	t.Logf("Remove result: %s", result)

	cfg := readSequenceFile(t, env)
	if len(cfg.Sequence) != 1 {
		t.Fatalf("expected 1 account after removal, got %d", len(cfg.Sequence))
	}
	if _, ok := cfg.Accounts["2"]; ok {
		t.Error("account 2 (bob) should have been removed")
	}
}

func TestFullIntegrationAddSwitchVerify(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	// Add account A.
	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")
	cfg := readSequenceFile(t, env)
	if *cfg.ActiveAccountNumber != 1 {
		t.Fatalf("after add A: active = %d, want 1", *cfg.ActiveAccountNumber)
	}

	// Add account B.
	addAccountFixture(t, sw, env, "bob@example.com", "org-2", "OrgTwo", "tok-bob")
	cfg = readSequenceFile(t, env)
	if len(cfg.Sequence) != 2 {
		t.Fatalf("after add B: sequence len = %d, want 2", len(cfg.Sequence))
	}
	restoreActiveFiles(t, env)

	// Switch to B.
	if err := sw.Switch(); err != nil {
		t.Fatalf("Switch A->B: %v", err)
	}
	cfg = readSequenceFile(t, env)
	if *cfg.ActiveAccountNumber != 2 {
		t.Fatalf("after switch to B: active = %d, want 2", *cfg.ActiveAccountNumber)
	}

	// Verify B's credentials are active.
	activeCreds, err := credential.ReadActiveCredentials()
	if err != nil {
		t.Fatalf("ReadActiveCredentials: %v", err)
	}
	assertTokenInCreds(t, activeCreds, "tok-bob")

	// Verify B's oauthAccount in claude.json.
	claudeCfg, err := config.ReadClaudeConfig(env.claudeConfigPath)
	if err != nil {
		t.Fatalf("ReadClaudeConfig: %v", err)
	}
	assertOAuthEmail(t, claudeCfg, "bob@example.com")

	// Switch back to A.
	if err := sw.Switch(); err != nil {
		t.Fatalf("Switch B->A: %v", err)
	}
	cfg = readSequenceFile(t, env)
	if *cfg.ActiveAccountNumber != 1 {
		t.Fatalf("after switch back to A: active = %d, want 1", *cfg.ActiveAccountNumber)
	}

	// Verify A's credentials are active.
	activeCreds, err = credential.ReadActiveCredentials()
	if err != nil {
		t.Fatalf("ReadActiveCredentials: %v", err)
	}
	assertTokenInCreds(t, activeCreds, "tok-alice")

	// Verify A's oauthAccount in claude.json.
	claudeCfg, err = config.ReadClaudeConfig(env.claudeConfigPath)
	if err != nil {
		t.Fatalf("ReadClaudeConfig: %v", err)
	}
	assertOAuthEmail(t, claudeCfg, "alice@example.com")

	// List should show both accounts.
	list, err := sw.ListAccounts()
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	t.Logf("Final list:\n%s", list)

	// Status should show alice as active.
	status, err := sw.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	t.Logf("Final status:\n%s", status)
}

func TestAddMultipleThenListOrder(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	names := []struct {
		email, orgUUID, orgName, token string
	}{
		{"alice@example.com", "org-1", "OrgOne", "tok-alice"},
		{"bob@example.com", "org-2", "OrgTwo", "tok-bob"},
		{"charlie@example.com", "org-3", "OrgThree", "tok-charlie"},
	}

	for _, n := range names {
		addAccountFixture(t, sw, env, n.email, n.orgUUID, n.orgName, n.token)
	}

	cfg := readSequenceFile(t, env)
	if len(cfg.Sequence) != 3 {
		t.Fatalf("expected 3 accounts, got %d", len(cfg.Sequence))
	}

	// Verify sequence ordering.
	for i, expected := range []int{1, 2, 3} {
		if cfg.Sequence[i] != expected {
			t.Errorf("Sequence[%d] = %d, want %d", i, cfg.Sequence[i], expected)
		}
	}

	// Verify account numbers are correct.
	for i, n := range names {
		key := strconv.Itoa(i + 1)
		acct, ok := cfg.Accounts[key]
		if !ok {
			t.Errorf("account %s not found", key)
			continue
		}
		if acct.Email != n.email {
			t.Errorf("account %s email = %q, want %q", key, acct.Email, n.email)
		}
	}
}

func TestSwitchBackAndForthCredentialsIntegrity(t *testing.T) {
	env := setupTestEnv(t)
	sw := newTestSwitcher(t, env, fakeHTTPClient())

	addAccountFixture(t, sw, env, "alice@example.com", "org-1", "OrgOne", "tok-alice")
	addAccountFixture(t, sw, env, "bob@example.com", "org-2", "OrgTwo", "tok-bob")
	restoreActiveFiles(t, env)

	// Switch several times and verify credentials integrity each time.
	for i := range 4 {
		if err := sw.Switch(); err != nil {
			t.Fatalf("Switch iteration %d: %v", i, err)
		}

		cfg := readSequenceFile(t, env)
		activeNum := *cfg.ActiveAccountNumber
		activeKey := strconv.Itoa(activeNum)
		acct := cfg.Accounts[activeKey]

		activeCreds, err := credential.ReadActiveCredentials()
		if err != nil {
			t.Fatalf("ReadActiveCredentials iteration %d: %v", i, err)
		}

		expectedToken := fmt.Sprintf("tok-%s", firstPart(acct.Email))
		assertTokenInCreds(t, activeCreds, expectedToken)
	}
}

// assertTokenInCreds checks that the given credentials JSON contains the expected access token.
func assertTokenInCreds(t *testing.T, credsJSON, expectedToken string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(credsJSON), &m); err != nil {
		t.Fatalf("parsing creds JSON: %v", err)
	}
	oauthMap, ok := m["claudeAiOauth"].(map[string]any)
	if !ok {
		t.Fatal("creds missing claudeAiOauth")
	}
	if oauthMap["accessToken"] != expectedToken {
		t.Errorf("accessToken = %v, want %s", oauthMap["accessToken"], expectedToken)
	}
}

// assertOAuthEmail checks that the oauthAccount in a claude config has the expected email.
func assertOAuthEmail(t *testing.T, claudeCfg map[string]any, expectedEmail string) {
	t.Helper()
	oaRaw, ok := claudeCfg["oauthAccount"]
	if !ok {
		t.Fatal("claude config missing oauthAccount")
	}
	oaBytes, _ := json.Marshal(oaRaw)
	var oa models.OAuthAccount
	if err := json.Unmarshal(oaBytes, &oa); err != nil {
		t.Fatalf("parsing oauthAccount: %v", err)
	}
	if oa.EmailAddress != expectedEmail {
		t.Errorf("oauthAccount email = %q, want %q", oa.EmailAddress, expectedEmail)
	}
}

// firstPart returns the local part of an email address (before the @).
func firstPart(email string) string {
	parts := splitEmail(email)
	if len(parts) > 0 {
		return parts[0]
	}
	return email
}

func splitEmail(email string) []string {
	idx := 0
	for i, c := range email {
		if c == '@' {
			idx = i
			break
		}
	}
	if idx == 0 {
		return []string{email}
	}
	return []string{email[:idx], email[idx+1:]}
}
