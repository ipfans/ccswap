package credential_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ipfans/ccswap/pkg/credential"
)

func TestWriteThenReadCredentials(t *testing.T) {
	dir := t.TempDir()
	store, err := credential.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	input := `{"claudeAiOauth":{"accessToken":"test123","refreshToken":"rt456"}}`
	if err := store.WriteAccountCredentials(1, "user@example.com", input); err != nil {
		t.Fatalf("WriteAccountCredentials: %v", err)
	}

	got, err := store.ReadAccountCredentials(1, "user@example.com")
	if err != nil {
		t.Fatalf("ReadAccountCredentials: %v", err)
	}
	if got != input {
		t.Errorf("round-trip mismatch:\n  got:  %q\n  want: %q", got, input)
	}
}

func TestBase64EncodingMatchesPython(t *testing.T) {
	// Python: base64.b64encode(b'{"claudeAiOauth":{"accessToken":"test123"}}')
	// produces b'eyJjbGF1ZGVBaU9hdXRoIjp7ImFjY2Vzc1Rva2VuIjoidGVzdDEyMyJ9fQ=='
	input := `{"claudeAiOauth":{"accessToken":"test123"}}`
	want := "eyJjbGF1ZGVBaU9hdXRoIjp7ImFjY2Vzc1Rva2VuIjoidGVzdDEyMyJ9fQ=="

	got := base64.StdEncoding.EncodeToString([]byte(input))
	if got != want {
		t.Errorf("base64 encoding mismatch:\n  got:  %q\n  want: %q", got, want)
	}

	// Verify the stored file contains the expected base64.
	dir := t.TempDir()
	store, err := credential.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if err := store.WriteAccountCredentials(1, "test@example.com", input); err != nil {
		t.Fatalf("WriteAccountCredentials: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "credentials", ".creds-1-test@example.com.enc"))
	if err != nil {
		t.Fatalf("reading raw file: %v", err)
	}
	if string(raw) != want {
		t.Errorf("on-disk base64 mismatch:\n  got:  %q\n  want: %q", string(raw), want)
	}
}

func TestDeleteThenReadReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	store, err := credential.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	input := `{"some":"data"}`
	if err := store.WriteAccountCredentials(2, "del@example.com", input); err != nil {
		t.Fatalf("WriteAccountCredentials: %v", err)
	}

	if err := store.DeleteAccountCredentials(2, "del@example.com"); err != nil {
		t.Fatalf("DeleteAccountCredentials: %v", err)
	}

	got, err := store.ReadAccountCredentials(2, "del@example.com")
	if err != nil {
		t.Fatalf("ReadAccountCredentials after delete: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string after delete, got %q", got)
	}
}

func TestReadNonexistentReturnsEmptyNil(t *testing.T) {
	dir := t.TempDir()
	store, err := credential.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	got, err := store.ReadAccountCredentials(99, "nobody@example.com")
	if err != nil {
		t.Fatalf("expected nil error for nonexistent creds, got: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string for nonexistent creds, got %q", got)
	}

	gotCfg, err := store.ReadAccountConfig(99, "nobody@example.com")
	if err != nil {
		t.Fatalf("expected nil error for nonexistent config, got: %v", err)
	}
	if gotCfg != "" {
		t.Errorf("expected empty string for nonexistent config, got %q", gotCfg)
	}
}

func TestWriteToReadOnlyDirReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod not reliable on Windows")
	}

	dir := t.TempDir()
	store, err := credential.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Make the credentials directory read-only.
	credsDir := filepath.Join(dir, "credentials")
	if err := os.Chmod(credsDir, 0555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(credsDir, 0755) })

	err = store.WriteAccountCredentials(1, "fail@example.com", `{"key":"val"}`)
	if err == nil {
		t.Fatal("expected error writing to read-only directory, got nil")
	}
}

func TestWriteThenReadConfigBackup(t *testing.T) {
	dir := t.TempDir()
	store, err := credential.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	configJSON := `{"oauthAccount":{"emailAddress":"user@example.com"},"numericSetting":42}`
	if err := store.WriteAccountConfig(1, "user@example.com", configJSON); err != nil {
		t.Fatalf("WriteAccountConfig: %v", err)
	}

	got, err := store.ReadAccountConfig(1, "user@example.com")
	if err != nil {
		t.Fatalf("ReadAccountConfig: %v", err)
	}
	if got != configJSON {
		t.Errorf("config round-trip mismatch:\n  got:  %q\n  want: %q", got, configJSON)
	}

	// Verify config is stored as plain text (no base64).
	raw, err := os.ReadFile(filepath.Join(dir, "configs", ".claude-config-1-user@example.com.json"))
	if err != nil {
		t.Fatalf("reading raw config file: %v", err)
	}
	if string(raw) != configJSON {
		t.Errorf("on-disk config should be plain JSON, got %q", string(raw))
	}
}

func TestDeleteAccountFilesRemovesBoth(t *testing.T) {
	dir := t.TempDir()
	store, err := credential.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	creds := `{"claudeAiOauth":{"accessToken":"tok"}}`
	config := `{"oauthAccount":{"emailAddress":"both@example.com"}}`

	if err := store.WriteAccountCredentials(3, "both@example.com", creds); err != nil {
		t.Fatalf("WriteAccountCredentials: %v", err)
	}
	if err := store.WriteAccountConfig(3, "both@example.com", config); err != nil {
		t.Fatalf("WriteAccountConfig: %v", err)
	}

	// Verify both files exist before deletion.
	credFile := filepath.Join(dir, "credentials", ".creds-3-both@example.com.enc")
	cfgFile := filepath.Join(dir, "configs", ".claude-config-3-both@example.com.json")

	if _, err := os.Stat(credFile); err != nil {
		t.Fatalf("credential file should exist: %v", err)
	}
	if _, err := os.Stat(cfgFile); err != nil {
		t.Fatalf("config file should exist: %v", err)
	}

	if err := store.DeleteAccountFiles(3, "both@example.com"); err != nil {
		t.Fatalf("DeleteAccountFiles: %v", err)
	}

	// Both files should be gone.
	if _, err := os.Stat(credFile); !os.IsNotExist(err) {
		t.Error("credential file should not exist after DeleteAccountFiles")
	}
	if _, err := os.Stat(cfgFile); !os.IsNotExist(err) {
		t.Error("config file should not exist after DeleteAccountFiles")
	}

	// Reading after deletion should return empty strings.
	gotCreds, err := store.ReadAccountCredentials(3, "both@example.com")
	if err != nil {
		t.Fatalf("ReadAccountCredentials after delete: %v", err)
	}
	if gotCreds != "" {
		t.Errorf("expected empty creds after delete, got %q", gotCreds)
	}

	gotCfg, err := store.ReadAccountConfig(3, "both@example.com")
	if err != nil {
		t.Fatalf("ReadAccountConfig after delete: %v", err)
	}
	if gotCfg != "" {
		t.Errorf("expected empty config after delete, got %q", gotCfg)
	}
}

func TestActiveCredentialsRoundTrip(t *testing.T) {
	// Use a temp dir as the fake home to avoid touching the real ~/.claude/.
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	credsJSON := `{"claudeAiOauth":{"accessToken":"active-token","refreshToken":"active-refresh"}}`

	if err := credential.WriteActiveCredentials(credsJSON); err != nil {
		t.Fatalf("WriteActiveCredentials: %v", err)
	}

	got, err := credential.ReadActiveCredentials()
	if err != nil {
		t.Fatalf("ReadActiveCredentials: %v", err)
	}
	if got != credsJSON {
		t.Errorf("active credentials round-trip mismatch:\n  got:  %q\n  want: %q", got, credsJSON)
	}

	// Verify the file was written atomically to the correct location.
	target := filepath.Join(fakeHome, ".claude", ".credentials.json")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected file at %s: %v", target, err)
	}
}

func TestFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission checks not reliable on Windows")
	}

	dir := t.TempDir()
	store, err := credential.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Write credential file and check permissions.
	if err := store.WriteAccountCredentials(1, "perm@example.com", `{"key":"val"}`); err != nil {
		t.Fatalf("WriteAccountCredentials: %v", err)
	}

	credFile := filepath.Join(dir, "credentials", ".creds-1-perm@example.com.enc")
	info, err := os.Stat(credFile)
	if err != nil {
		t.Fatalf("stat credential file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("credential file permissions = %04o, want 0600", perm)
	}

	// Write config file and check permissions.
	if err := store.WriteAccountConfig(1, "perm@example.com", `{"setting":"value"}`); err != nil {
		t.Fatalf("WriteAccountConfig: %v", err)
	}

	cfgFile := filepath.Join(dir, "configs", ".claude-config-1-perm@example.com.json")
	info, err = os.Stat(cfgFile)
	if err != nil {
		t.Fatalf("stat config file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("config file permissions = %04o, want 0600", perm)
	}

	// Write active credentials and check permissions.
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	if err := credential.WriteActiveCredentials(`{"active":"creds"}`); err != nil {
		t.Fatalf("WriteActiveCredentials: %v", err)
	}

	activeFile := filepath.Join(fakeHome, ".claude", ".credentials.json")
	info, err = os.Stat(activeFile)
	if err != nil {
		t.Fatalf("stat active credentials file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("active credentials file permissions = %04o, want 0600", perm)
	}
}
