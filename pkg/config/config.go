package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ipfans/ccswap/pkg/models"
)

// readJSON reads a JSON file at path and decodes it into a value of type T.
func readJSON[T any](path string) (T, error) {
	var zero T
	f, err := os.Open(path)
	if err != nil {
		return zero, err
	}
	defer f.Close()

	var v T
	if err := json.NewDecoder(f).Decode(&v); err != nil {
		return zero, fmt.Errorf("decoding %s: %w", path, err)
	}
	return v, nil
}

// writeJSON atomically writes data as indented JSON to path.
// It creates a temp file in the same directory, writes JSON, then renames.
// On non-Windows platforms the temp file is chmod 0600 before rename.
func writeJSON(path string, data any) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()

	// Clean up temp file on any error.
	success := false
	defer func() {
		if !success {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		return fmt.Errorf("encoding JSON: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpName, 0600); err != nil {
			return fmt.Errorf("chmod temp file: %w", err)
		}
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("renaming temp to target: %w", err)
	}
	success = true
	return nil
}

// ReadSwapConfig reads a sequence.json file and returns the parsed SwapConfig.
func ReadSwapConfig(path string) (*models.SwapConfig, error) {
	cfg, err := readJSON[models.SwapConfig](path)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

// WriteSwapConfig atomically writes a SwapConfig to the given path as JSON.
func WriteSwapConfig(path string, cfg *models.SwapConfig) error {
	return writeJSON(path, cfg)
}

// ReadClaudeConfig reads a claude.json file as a raw map.
// It uses json.Decoder with UseNumber() to preserve integer types
// as json.Number instead of converting them to float64.
func ReadClaudeConfig(path string) (map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.UseNumber()

	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", path, err)
	}
	return m, nil
}

// WriteClaudeConfig atomically writes a claude config map to the given path.
func WriteClaudeConfig(path string, data map[string]any) error {
	return writeJSON(path, data)
}

// GetClaudeConfigPath returns the path to the Claude configuration file.
// If the CLAUDE_CONFIG_DIR environment variable is set, it returns
// that directory joined with "claude.json". Otherwise it returns
// ~/.claude.json.
func GetClaudeConfigPath() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "claude.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".claude.json")
	}
	return filepath.Join(home, ".claude.json")
}

// GetBackupRoot returns the path to the ccswap backup directory (~/.claude-swap/).
func GetBackupRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".claude-swap")
	}
	return filepath.Join(home, ".claude-swap")
}
