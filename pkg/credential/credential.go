package credential

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Store provides file-based credential and config backup storage.
// Credentials are base64-encoded; config backups are stored as plain JSON.
type Store struct {
	credentialsDir string // e.g. ~/.claude-swap/credentials/
	configsDir     string // e.g. ~/.claude-swap/configs/
}

// NewStore creates a Store rooted at backupRoot.
// It creates the credentials/ and configs/ subdirectories if they don't exist.
func NewStore(backupRoot string) (*Store, error) {
	credsDir := filepath.Join(backupRoot, "credentials")
	cfgsDir := filepath.Join(backupRoot, "configs")

	if err := os.MkdirAll(credsDir, 0700); err != nil {
		return nil, fmt.Errorf("creating credentials dir: %w", err)
	}
	if err := os.MkdirAll(cfgsDir, 0700); err != nil {
		return nil, fmt.Errorf("creating configs dir: %w", err)
	}

	return &Store{
		credentialsDir: credsDir,
		configsDir:     cfgsDir,
	}, nil
}

// credPath returns the file path for an account's credential backup.
func (s *Store) credPath(accountNum int, email string) string {
	name := fmt.Sprintf(".creds-%d-%s.enc", accountNum, email)
	return filepath.Join(s.credentialsDir, name)
}

// configPath returns the file path for an account's config backup.
func (s *Store) configPath(accountNum int, email string) string {
	name := fmt.Sprintf(".claude-config-%d-%s.json", accountNum, email)
	return filepath.Join(s.configsDir, name)
}

// ReadAccountCredentials reads a credential backup file, base64-decodes
// its content, and returns the decoded string.
// If the file does not exist, it returns ("", nil).
func (s *Store) ReadAccountCredentials(accountNum int, email string) (string, error) {
	data, err := os.ReadFile(s.credPath(accountNum, email))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	decoded, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	return string(decoded), nil
}

// WriteAccountCredentials base64-encodes credsJSON and writes it to
// the credential backup file. On non-Windows platforms the file is
// chmod 0600.
func (s *Store) WriteAccountCredentials(accountNum int, email string, credsJSON string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(credsJSON))
	path := s.credPath(accountNum, email)

	if err := os.WriteFile(path, []byte(encoded), 0600); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return os.Chmod(path, 0600)
	}
	return nil
}

// DeleteAccountCredentials removes the credential backup file.
// If the file does not exist, no error is returned.
func (s *Store) DeleteAccountCredentials(accountNum int, email string) error {
	err := os.Remove(s.credPath(accountNum, email))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ReadAccountConfig reads a config backup file and returns its content.
// No base64 decoding is applied. If the file does not exist, it returns
// ("", nil).
func (s *Store) ReadAccountConfig(accountNum int, email string) (string, error) {
	data, err := os.ReadFile(s.configPath(accountNum, email))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// WriteAccountConfig writes configJSON to the config backup file.
// On non-Windows platforms the file is chmod 0600.
func (s *Store) WriteAccountConfig(accountNum int, email string, configJSON string) error {
	path := s.configPath(accountNum, email)

	if err := os.WriteFile(path, []byte(configJSON), 0600); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return os.Chmod(path, 0600)
	}
	return nil
}

// DeleteAccountFiles removes both the credential and config backup files
// for the given account. Missing files are silently ignored.
func (s *Store) DeleteAccountFiles(accountNum int, email string) error {
	if err := s.DeleteAccountCredentials(accountNum, email); err != nil {
		return err
	}

	err := os.Remove(s.configPath(accountNum, email))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ReadActiveCredentials reads the active Claude credentials file at
// ~/.claude/.credentials.json and returns its content.
func ReadActiveCredentials() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determining home directory: %w", err)
	}
	path := filepath.Join(home, ".claude", ".credentials.json")

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// WriteActiveCredentials atomically writes credsJSON to
// ~/.claude/.credentials.json using a temp file + rename pattern.
// On non-Windows platforms the file is chmod 0600.
func WriteActiveCredentials(credsJSON string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("determining home directory: %w", err)
	}

	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating .claude dir: %w", err)
	}

	target := filepath.Join(dir, ".credentials.json")

	tmp, err := os.CreateTemp(dir, ".tmp-creds-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()

	success := false
	defer func() {
		if !success {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.WriteString(credsJSON); err != nil {
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpName, 0600); err != nil {
			return fmt.Errorf("chmod temp file: %w", err)
		}
	}

	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("renaming temp to target: %w", err)
	}
	success = true
	return nil
}
