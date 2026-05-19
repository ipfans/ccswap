package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ipfans/ccswap/pkg/config"
	"github.com/ipfans/ccswap/pkg/models"
)

func TestSwapConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sequence.json")

	active := 2
	original := &models.SwapConfig{
		ActiveAccountNumber: &active,
		Sequence:            []int{1, 2, 3},
		Accounts: map[string]models.Account{
			"1": {
				Email:            "alice@example.com",
				UUID:             "uuid-1",
				OrganizationUUID: "org-uuid-1",
				OrganizationName: "Org One",
				Added:            "2025-01-01T00:00:00Z",
			},
			"2": {
				Email:            "bob@example.com",
				UUID:             "uuid-2",
				OrganizationUUID: "org-uuid-2",
				OrganizationName: "Org Two",
				Added:            "2025-01-02T00:00:00Z",
			},
		},
		LastUpdated: "2025-06-01T12:00:00Z",
	}

	if err := config.WriteSwapConfig(path, original); err != nil {
		t.Fatalf("WriteSwapConfig: %v", err)
	}

	got, err := config.ReadSwapConfig(path)
	if err != nil {
		t.Fatalf("ReadSwapConfig: %v", err)
	}

	// Check ActiveAccountNumber pointer value.
	if got.ActiveAccountNumber == nil {
		t.Fatal("ActiveAccountNumber is nil, expected pointer to 2")
	}
	if *got.ActiveAccountNumber != active {
		t.Errorf("ActiveAccountNumber = %d, want %d", *got.ActiveAccountNumber, active)
	}

	// Check Sequence.
	if len(got.Sequence) != 3 {
		t.Fatalf("Sequence length = %d, want 3", len(got.Sequence))
	}
	for i, v := range []int{1, 2, 3} {
		if got.Sequence[i] != v {
			t.Errorf("Sequence[%d] = %d, want %d", i, got.Sequence[i], v)
		}
	}

	// Check Accounts.
	if len(got.Accounts) != 2 {
		t.Fatalf("Accounts length = %d, want 2", len(got.Accounts))
	}
	acct1, ok := got.Accounts["1"]
	if !ok {
		t.Fatal("Accounts missing key '1'")
	}
	if acct1.Email != "alice@example.com" {
		t.Errorf("Accounts['1'].Email = %q, want %q", acct1.Email, "alice@example.com")
	}

	// Check LastUpdated.
	if got.LastUpdated != original.LastUpdated {
		t.Errorf("LastUpdated = %q, want %q", got.LastUpdated, original.LastUpdated)
	}
}

func TestAtomicWriteDoesNotCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sequence.json")

	// Write initial content.
	active := 1
	initial := &models.SwapConfig{
		ActiveAccountNumber: &active,
		Sequence:            []int{1},
		Accounts: map[string]models.Account{
			"1": {Email: "first@example.com"},
		},
		LastUpdated: "2025-01-01T00:00:00Z",
	}
	if err := config.WriteSwapConfig(path, initial); err != nil {
		t.Fatalf("initial WriteSwapConfig: %v", err)
	}

	// Overwrite with new content.
	active2 := 2
	updated := &models.SwapConfig{
		ActiveAccountNumber: &active2,
		Sequence:            []int{1, 2},
		Accounts: map[string]models.Account{
			"1": {Email: "first@example.com"},
			"2": {Email: "second@example.com"},
		},
		LastUpdated: "2025-06-01T00:00:00Z",
	}
	if err := config.WriteSwapConfig(path, updated); err != nil {
		t.Fatalf("updated WriteSwapConfig: %v", err)
	}

	// Read back and verify the file is valid JSON with the updated content.
	got, err := config.ReadSwapConfig(path)
	if err != nil {
		t.Fatalf("ReadSwapConfig after update: %v", err)
	}
	if *got.ActiveAccountNumber != 2 {
		t.Errorf("ActiveAccountNumber = %d after update, want 2", *got.ActiveAccountNumber)
	}
	if len(got.Accounts) != 2 {
		t.Errorf("Accounts length = %d after update, want 2", len(got.Accounts))
	}
}

func TestReadPythonCompatibleSequenceJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sequence.json")

	// Fixture matching the Python version's output format.
	fixture := `{
  "activeAccountNumber": 1,
  "sequence": [1, 2, 3],
  "accounts": {
    "1": {
      "email": "user1@example.com",
      "uuid": "aaa-bbb-ccc",
      "organizationUuid": "org-111",
      "organizationName": "My Org",
      "added": "2025-03-15T10:00:00Z"
    },
    "2": {
      "email": "user2@example.com",
      "uuid": "ddd-eee-fff",
      "organizationUuid": "org-222",
      "organizationName": "Other Org",
      "added": "2025-04-20T14:30:00Z"
    },
    "3": {
      "email": "user3@example.com",
      "uuid": "ggg-hhh-iii",
      "organizationUuid": "org-333",
      "organizationName": "Third Org",
      "added": "2025-05-01T08:00:00Z"
    }
  },
  "lastUpdated": "2025-05-10T16:45:00Z"
}`

	if err := os.WriteFile(path, []byte(fixture), 0644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	cfg, err := config.ReadSwapConfig(path)
	if err != nil {
		t.Fatalf("ReadSwapConfig: %v", err)
	}

	if cfg.ActiveAccountNumber == nil || *cfg.ActiveAccountNumber != 1 {
		t.Errorf("ActiveAccountNumber: got %v, want pointer to 1", cfg.ActiveAccountNumber)
	}
	if len(cfg.Sequence) != 3 {
		t.Errorf("Sequence length = %d, want 3", len(cfg.Sequence))
	}
	if len(cfg.Accounts) != 3 {
		t.Errorf("Accounts length = %d, want 3", len(cfg.Accounts))
	}
	acct3, ok := cfg.Accounts["3"]
	if !ok {
		t.Fatal("missing account key '3'")
	}
	if acct3.Email != "user3@example.com" {
		t.Errorf("Accounts['3'].Email = %q, want %q", acct3.Email, "user3@example.com")
	}
}

func TestReadNonexistentFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.json")

	_, err := config.ReadSwapConfig(path)
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}

	_, err = config.ReadClaudeConfig(path)
	if err == nil {
		t.Fatal("expected error for nonexistent claude config, got nil")
	}
}

func TestReadInvalidJSONReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")

	if err := os.WriteFile(path, []byte("{not valid json!!!"), 0644); err != nil {
		t.Fatalf("writing bad fixture: %v", err)
	}

	_, err := config.ReadSwapConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON (SwapConfig), got nil")
	}

	_, err = config.ReadClaudeConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON (ClaudeConfig), got nil")
	}
}

func TestClaudeConfigDirEnvVar(t *testing.T) {
	customDir := "/tmp/custom-claude-dir"
	t.Setenv("CLAUDE_CONFIG_DIR", customDir)

	got := config.GetClaudeConfigPath()
	want := filepath.Join(customDir, "claude.json")
	if got != want {
		t.Errorf("GetClaudeConfigPath() = %q, want %q", got, want)
	}
}

func TestClaudeConfigPathDefault(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	got := config.GetClaudeConfigPath()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	want := filepath.Join(home, ".claude.json")
	if got != want {
		t.Errorf("GetClaudeConfigPath() = %q, want %q", got, want)
	}
}

func TestWriteToReadOnlyDirReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod not reliable on Windows")
	}

	dir := t.TempDir()
	roDir := filepath.Join(dir, "readonly")
	if err := os.MkdirAll(roDir, 0555); err != nil {
		t.Fatalf("creating read-only dir: %v", err)
	}
	// Ensure cleanup can remove the dir.
	t.Cleanup(func() { os.Chmod(roDir, 0755) })

	path := filepath.Join(roDir, "sequence.json")
	active := 1
	cfg := &models.SwapConfig{
		ActiveAccountNumber: &active,
		Sequence:            []int{1},
		Accounts:            map[string]models.Account{},
		LastUpdated:         "2025-01-01T00:00:00Z",
	}

	err := config.WriteSwapConfig(path, cfg)
	if err == nil {
		t.Fatal("expected error writing to read-only directory, got nil")
	}
}

func TestReadClaudeConfigPreservesNumberTypes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude.json")

	// Write JSON with integer and float values.
	content := `{
  "numericSetting": 42,
  "floatSetting": 3.14,
  "stringSetting": "hello",
  "boolSetting": true,
  "nested": {
    "count": 100
  }
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	m, err := config.ReadClaudeConfig(path)
	if err != nil {
		t.Fatalf("ReadClaudeConfig: %v", err)
	}

	// Verify integer values are json.Number, not float64.
	numVal, ok := m["numericSetting"]
	if !ok {
		t.Fatal("missing key 'numericSetting'")
	}
	jn, ok := numVal.(json.Number)
	if !ok {
		t.Fatalf("numericSetting type = %T, want json.Number", numVal)
	}
	if jn.String() != "42" {
		t.Errorf("numericSetting = %q, want %q", jn.String(), "42")
	}

	// Verify float values are also json.Number.
	floatVal, ok := m["floatSetting"]
	if !ok {
		t.Fatal("missing key 'floatSetting'")
	}
	jnf, ok := floatVal.(json.Number)
	if !ok {
		t.Fatalf("floatSetting type = %T, want json.Number", floatVal)
	}
	if jnf.String() != "3.14" {
		t.Errorf("floatSetting = %q, want %q", jnf.String(), "3.14")
	}

	// Verify nested integer is also preserved.
	nested, ok := m["nested"].(map[string]any)
	if !ok {
		t.Fatal("nested is not a map[string]any")
	}
	countVal, ok := nested["count"]
	if !ok {
		t.Fatal("missing nested key 'count'")
	}
	jnc, ok := countVal.(json.Number)
	if !ok {
		t.Fatalf("nested.count type = %T, want json.Number", countVal)
	}
	if jnc.String() != "100" {
		t.Errorf("nested.count = %q, want %q", jnc.String(), "100")
	}

	// Verify non-numeric types are unaffected.
	strVal, ok := m["stringSetting"].(string)
	if !ok {
		t.Fatalf("stringSetting type = %T, want string", m["stringSetting"])
	}
	if strVal != "hello" {
		t.Errorf("stringSetting = %q, want %q", strVal, "hello")
	}

	boolVal, ok := m["boolSetting"].(bool)
	if !ok {
		t.Fatalf("boolSetting type = %T, want bool", m["boolSetting"])
	}
	if !boolVal {
		t.Error("boolSetting = false, want true")
	}
}
