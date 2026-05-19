package switcher

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ipfans/ccswap/pkg/config"
	"github.com/ipfans/ccswap/pkg/credential"
	"github.com/ipfans/ccswap/pkg/lock"
	"github.com/ipfans/ccswap/pkg/models"
	"github.com/ipfans/ccswap/pkg/oauth"
)

// Switcher provides account add/remove/list/status/switch operations.
type Switcher struct {
	backupRoot      string
	sequenceFile    string
	lockFile        string
	claudeConfigPath string
	store           *credential.Store
	usageCache      *oauth.UsageCache
	httpClient      *http.Client
}

// NewSwitcher creates a Switcher using default paths derived from
// config.GetBackupRoot and config.GetClaudeConfigPath.
func NewSwitcher() (*Switcher, error) {
	root := config.GetBackupRoot()
	return NewSwitcherWithPaths(root, config.GetClaudeConfigPath(), nil)
}

// NewSwitcherWithPaths creates a Switcher with explicit paths and an optional
// HTTP client, which is useful for testing.
func NewSwitcherWithPaths(backupRoot, claudeConfigPath string, httpClient *http.Client) (*Switcher, error) {
	if err := os.MkdirAll(backupRoot, 0700); err != nil {
		return nil, fmt.Errorf("creating backup root: %w", err)
	}

	store, err := credential.NewStore(backupRoot)
	if err != nil {
		return nil, fmt.Errorf("creating credential store: %w", err)
	}

	cacheDir := filepath.Join(backupRoot, "cache")
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return nil, fmt.Errorf("creating cache dir: %w", err)
	}

	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	return &Switcher{
		backupRoot:       backupRoot,
		sequenceFile:     filepath.Join(backupRoot, "sequence.json"),
		lockFile:         filepath.Join(backupRoot, ".lock"),
		claudeConfigPath: claudeConfigPath,
		store:            store,
		usageCache:       oauth.NewUsageCache(cacheDir, 30*time.Second),
		httpClient:       httpClient,
	}, nil
}

// readSequence reads the sequence.json file. If the file does not exist,
// it returns a fresh empty SwapConfig.
func (s *Switcher) readSequence() (*models.SwapConfig, error) {
	cfg, err := config.ReadSwapConfig(s.sequenceFile)
	if err != nil {
		if os.IsNotExist(err) {
			return &models.SwapConfig{
				Accounts: make(map[string]models.Account),
			}, nil
		}
		return nil, fmt.Errorf("reading sequence file: %w", err)
	}
	if cfg.Accounts == nil {
		cfg.Accounts = make(map[string]models.Account)
	}
	return cfg, nil
}

// writeSequence writes the SwapConfig to sequence.json, updating LastUpdated.
func (s *Switcher) writeSequence(cfg *models.SwapConfig) error {
	cfg.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	return config.WriteSwapConfig(s.sequenceFile, cfg)
}

// resolveIdentifier resolves a string identifier (account number or email)
// to an account number within the given SwapConfig.
func resolveIdentifier(cfg *models.SwapConfig, identifier string) (int, error) {
	if num, err := strconv.Atoi(identifier); err == nil {
		// Verify the number exists in accounts.
		key := strconv.Itoa(num)
		if _, ok := cfg.Accounts[key]; !ok {
			return 0, fmt.Errorf("account #%d not found", num)
		}
		return num, nil
	}

	// Search by email.
	var matches []int
	for key, acct := range cfg.Accounts {
		if acct.Email == identifier {
			num, _ := strconv.Atoi(key)
			matches = append(matches, num)
		}
	}

	switch len(matches) {
	case 0:
		return 0, fmt.Errorf("account not found: %s", identifier)
	case 1:
		return matches[0], nil
	default:
		return 0, fmt.Errorf("multiple accounts match email %q (accounts %v), use account number instead", identifier, matches)
	}
}

// nextAccountNumber returns max(existing account numbers) + 1, or 1 if none exist.
func nextAccountNumber(cfg *models.SwapConfig) int {
	max := 0
	for key := range cfg.Accounts {
		if num, err := strconv.Atoi(key); err == nil && num > max {
			max = num
		}
	}
	return max + 1
}

// AddAccount reads the current Claude config and active credentials,
// then adds (or updates) the account in the swap config.
func (s *Switcher) AddAccount() (string, error) {
	// Read claude.json to get oauthAccount info.
	claudeCfg, err := config.ReadClaudeConfig(s.claudeConfigPath)
	if err != nil {
		return "", fmt.Errorf("reading claude config: %w", err)
	}

	oauthAccountRaw, ok := claudeCfg["oauthAccount"]
	if !ok {
		return "", fmt.Errorf("claude config missing oauthAccount field")
	}

	// Marshal then unmarshal to get typed OAuthAccount.
	oaBytes, err := json.Marshal(oauthAccountRaw)
	if err != nil {
		return "", fmt.Errorf("marshaling oauthAccount: %w", err)
	}
	var oa models.OAuthAccount
	if err := json.Unmarshal(oaBytes, &oa); err != nil {
		return "", fmt.Errorf("parsing oauthAccount: %w", err)
	}

	if oa.EmailAddress == "" {
		return "", fmt.Errorf("oauthAccount has empty emailAddress")
	}

	// Read active credentials.
	credsJSON, err := credential.ReadActiveCredentials()
	if err != nil {
		return "", fmt.Errorf("reading active credentials: %w", err)
	}

	// Read current swap config.
	cfg, err := s.readSequence()
	if err != nil {
		return "", err
	}

	// Check if account already exists (match by email + orgUuid).
	var existingNum int
	var found bool
	for key, acct := range cfg.Accounts {
		if acct.Email == oa.EmailAddress && acct.OrganizationUUID == oa.OrganizationUUID {
			num, _ := strconv.Atoi(key)
			existingNum = num
			found = true
			break
		}
	}

	if found {
		// Update credentials only.
		if err := s.store.WriteAccountCredentials(existingNum, oa.EmailAddress, credsJSON); err != nil {
			return "", fmt.Errorf("updating credentials: %w", err)
		}
		claudeCfgJSON, err := json.Marshal(claudeCfg)
		if err != nil {
			return "", fmt.Errorf("marshaling claude config: %w", err)
		}
		if err := s.store.WriteAccountConfig(existingNum, oa.EmailAddress, string(claudeCfgJSON)); err != nil {
			return "", fmt.Errorf("updating config backup: %w", err)
		}
		return fmt.Sprintf("Updated account #%d %s (%s)", existingNum, oa.EmailAddress, oa.OrganizationName), nil
	}

	// New account.
	num := nextAccountNumber(cfg)
	acct := models.Account{
		Email:            oa.EmailAddress,
		UUID:             oa.AccountUUID,
		OrganizationUUID: oa.OrganizationUUID,
		OrganizationName: oa.OrganizationName,
		Added:            time.Now().UTC().Format(time.RFC3339),
	}

	key := strconv.Itoa(num)
	cfg.Accounts[key] = acct
	cfg.Sequence = append(cfg.Sequence, num)

	// If this is the first account, set it as active.
	if len(cfg.Sequence) == 1 {
		cfg.ActiveAccountNumber = &num
	}

	// Write credentials and config backup.
	if err := s.store.WriteAccountCredentials(num, oa.EmailAddress, credsJSON); err != nil {
		return "", fmt.Errorf("writing credentials: %w", err)
	}
	claudeCfgJSON, err := json.Marshal(claudeCfg)
	if err != nil {
		return "", fmt.Errorf("marshaling claude config: %w", err)
	}
	if err := s.store.WriteAccountConfig(num, oa.EmailAddress, string(claudeCfgJSON)); err != nil {
		return "", fmt.Errorf("writing config backup: %w", err)
	}

	if err := s.writeSequence(cfg); err != nil {
		return "", fmt.Errorf("writing sequence: %w", err)
	}

	return fmt.Sprintf("Added account #%d %s (%s)", num, oa.EmailAddress, oa.OrganizationName), nil
}

// RemoveAccount removes an account identified by number or email.
func (s *Switcher) RemoveAccount(identifier string) (string, error) {
	cfg, err := s.readSequence()
	if err != nil {
		return "", err
	}

	num, err := resolveIdentifier(cfg, identifier)
	if err != nil {
		return "", err
	}

	key := strconv.Itoa(num)
	acct, ok := cfg.Accounts[key]
	if !ok {
		return "", fmt.Errorf("account #%d not found", num)
	}

	// Delete backup files.
	if err := s.store.DeleteAccountFiles(num, acct.Email); err != nil {
		return "", fmt.Errorf("deleting account files: %w", err)
	}

	// Remove from Accounts map.
	delete(cfg.Accounts, key)

	// Remove from Sequence slice.
	newSeq := make([]int, 0, len(cfg.Sequence))
	for _, n := range cfg.Sequence {
		if n != num {
			newSeq = append(newSeq, n)
		}
	}
	cfg.Sequence = newSeq

	// If removing the active account, clear it.
	if cfg.ActiveAccountNumber != nil && *cfg.ActiveAccountNumber == num {
		cfg.ActiveAccountNumber = nil
	}

	if err := s.writeSequence(cfg); err != nil {
		return "", fmt.Errorf("writing sequence: %w", err)
	}

	return fmt.Sprintf("Removed account #%d %s (%s)", num, acct.Email, acct.OrganizationName), nil
}

// ListAccounts returns a formatted list of all accounts with usage info.
func (s *Switcher) ListAccounts() (string, error) {
	cfg, err := s.readSequence()
	if err != nil {
		return "", err
	}

	if len(cfg.Sequence) == 0 {
		return "No accounts configured. Use 'ccswap add' to add an account.", nil
	}

	var lines []string
	for _, num := range cfg.Sequence {
		key := strconv.Itoa(num)
		acct, ok := cfg.Accounts[key]
		if !ok {
			continue
		}

		marker := " "
		isActive := cfg.ActiveAccountNumber != nil && *cfg.ActiveAccountNumber == num
		if isActive {
			marker = "*"
		}

		fiveH := "N/A"
		sevenD := "N/A"

		credsJSON, credErr := s.store.ReadAccountCredentials(num, acct.Email)
		if credErr == nil && credsJSON != "" {
			persistFn := func(updated string) error {
				return s.store.WriteAccountCredentials(num, acct.Email, updated)
			}
			usage, usageErr := oauth.FetchUsageWithCache(
				s.usageCache, true, num, acct.Email,
				credsJSON, isActive, persistFn, s.httpClient,
			)
			if usageErr == nil && usage != nil {
				if usage.FiveHour != nil {
					fiveH = fmt.Sprintf("%.0f%%", usage.FiveHour.Utilization*100)
				}
				if usage.SevenDay != nil {
					sevenD = fmt.Sprintf("%.0f%%", usage.SevenDay.Utilization*100)
				}
			}
		}

		addedDate := acct.Added
		if t, err := time.Parse(time.RFC3339, acct.Added); err == nil {
			addedDate = t.Format("2006-01-02")
		}

		line := fmt.Sprintf("%s #%d %s (%s) added:%s 5h:%s 7d:%s",
			marker, num, acct.Email, acct.OrganizationName, addedDate, fiveH, sevenD)
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n"), nil
}

// Status returns a formatted status string for the active account.
func (s *Switcher) Status() (string, error) {
	cfg, err := s.readSequence()
	if err != nil {
		return "", err
	}

	if cfg.ActiveAccountNumber == nil {
		return "No active account", nil
	}

	num := *cfg.ActiveAccountNumber
	key := strconv.Itoa(num)
	acct, ok := cfg.Accounts[key]
	if !ok {
		return "", fmt.Errorf("active account #%d not found in accounts", num)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Active: #%d %s (%s)\n", num, acct.Email, acct.OrganizationName)

	credsJSON, err := s.store.ReadAccountCredentials(num, acct.Email)
	if err != nil || credsJSON == "" {
		sb.WriteString("Usage: unavailable (no credentials)")
		return sb.String(), nil
	}

	persistFn := func(updated string) error {
		return s.store.WriteAccountCredentials(num, acct.Email, updated)
	}
	usage, usageErr := oauth.FetchUsageWithCache(
		s.usageCache, true, num, acct.Email,
		credsJSON, true, persistFn, s.httpClient,
	)
	if usageErr != nil {
		fmt.Fprintf(&sb, "Usage: unavailable (%v)", usageErr)
		return sb.String(), nil
	}

	if usage.FiveHour != nil {
		countdown, clock := oauth.FormatReset(usage.FiveHour.ResetsAt)
		fmt.Fprintf(&sb, "  5h: %.0f%% (resets in %s at %s)\n",
			usage.FiveHour.Utilization*100, countdown, clock)
	}
	if usage.SevenDay != nil {
		countdown, clock := oauth.FormatReset(usage.SevenDay.ResetsAt)
		fmt.Fprintf(&sb, "  7d: %.0f%% (resets in %s at %s)",
			usage.SevenDay.Utilization*100, countdown, clock)
	}

	return sb.String(), nil
}

// Switch rotates to the next account in the sequence.
func (s *Switcher) Switch() error {
	cfg, err := s.readSequence()
	if err != nil {
		return err
	}

	if len(cfg.Sequence) < 2 {
		return fmt.Errorf("only one account configured, nothing to switch to")
	}

	// Find current position.
	currentIdx := -1
	if cfg.ActiveAccountNumber != nil {
		for i, num := range cfg.Sequence {
			if num == *cfg.ActiveAccountNumber {
				currentIdx = i
				break
			}
		}
	}

	// Advance to next, wrapping around.
	nextIdx := (currentIdx + 1) % len(cfg.Sequence)
	targetNum := cfg.Sequence[nextIdx]

	return s.performSwitch(targetNum)
}

// SwitchTo switches to a specific account identified by number or email.
func (s *Switcher) SwitchTo(identifier string) error {
	cfg, err := s.readSequence()
	if err != nil {
		return err
	}

	num, err := resolveIdentifier(cfg, identifier)
	if err != nil {
		return err
	}

	// If already active, return early.
	if cfg.ActiveAccountNumber != nil && *cfg.ActiveAccountNumber == num {
		return nil
	}

	return s.performSwitch(num)
}

// snapshot holds raw file contents for rollback.
type snapshot struct {
	sequenceData []byte
	claudeData   []byte
	credsData    []byte
	hasSequence  bool
	hasClaude    bool
	hasCreds     bool
	credsPath    string
}

// takeSnapshot captures the current state of all files that performSwitch
// may modify. Missing files are recorded as absent rather than causing errors.
func (s *Switcher) takeSnapshot() *snapshot {
	snap := &snapshot{}

	if data, err := os.ReadFile(s.sequenceFile); err == nil {
		snap.sequenceData = data
		snap.hasSequence = true
	}

	if data, err := os.ReadFile(s.claudeConfigPath); err == nil {
		snap.claudeData = data
		snap.hasClaude = true
	}

	home, err := os.UserHomeDir()
	if err == nil {
		snap.credsPath = filepath.Join(home, ".claude", ".credentials.json")
		if data, err := os.ReadFile(snap.credsPath); err == nil {
			snap.credsData = data
			snap.hasCreds = true
		}
	}

	return snap
}

// rollback restores all files from the snapshot.
func (s *Switcher) rollback(snap *snapshot) {
	if snap.hasSequence {
		_ = os.WriteFile(s.sequenceFile, snap.sequenceData, 0600)
	}
	if snap.hasClaude {
		_ = os.WriteFile(s.claudeConfigPath, snap.claudeData, 0600)
	}
	if snap.hasCreds && snap.credsPath != "" {
		_ = os.WriteFile(snap.credsPath, snap.credsData, 0600)
	}
}

// performSwitch executes the core account switch transaction.
func (s *Switcher) performSwitch(targetNum int) error {
	// Acquire lock.
	unlock, err := lock.Acquire(s.lockFile, lock.DefaultTimeout)
	if err != nil {
		return fmt.Errorf("acquiring lock: %w", err)
	}
	defer unlock()

	// Take snapshot for rollback.
	snap := s.takeSnapshot()

	// Read current state.
	cfg, err := s.readSequence()
	if err != nil {
		return err
	}

	targetKey := strconv.Itoa(targetNum)
	targetAcct, ok := cfg.Accounts[targetKey]
	if !ok {
		return fmt.Errorf("target account #%d not found", targetNum)
	}

	// Backup current active account if one exists.
	if cfg.ActiveAccountNumber != nil {
		currentNum := *cfg.ActiveAccountNumber
		currentKey := strconv.Itoa(currentNum)
		if currentAcct, ok := cfg.Accounts[currentKey]; ok {
			// Read and backup current active credentials.
			currentCreds, err := credential.ReadActiveCredentials()
			if err == nil && currentCreds != "" {
				if writeErr := s.store.WriteAccountCredentials(currentNum, currentAcct.Email, currentCreds); writeErr != nil {
					s.rollback(snap)
					return fmt.Errorf("backing up current credentials: %w", writeErr)
				}
			}

			// Read and backup current claude config.
			currentClaudeJSON, err := os.ReadFile(s.claudeConfigPath)
			if err == nil {
				if writeErr := s.store.WriteAccountConfig(currentNum, currentAcct.Email, string(currentClaudeJSON)); writeErr != nil {
					s.rollback(snap)
					return fmt.Errorf("backing up current config: %w", writeErr)
				}
			}
		}
	}

	// Read target account's backup credentials.
	targetCreds, err := s.store.ReadAccountCredentials(targetNum, targetAcct.Email)
	if err != nil {
		s.rollback(snap)
		return fmt.Errorf("reading target credentials: %w", err)
	}
	if targetCreds == "" {
		s.rollback(snap)
		return fmt.Errorf("no backup credentials for account #%d (%s)", targetNum, targetAcct.Email)
	}

	// Read target account's config backup.
	targetConfigJSON, err := s.store.ReadAccountConfig(targetNum, targetAcct.Email)
	if err != nil {
		s.rollback(snap)
		return fmt.Errorf("reading target config: %w", err)
	}

	// Write target credentials as active.
	if err := credential.WriteActiveCredentials(targetCreds); err != nil {
		s.rollback(snap)
		return fmt.Errorf("writing active credentials: %w", err)
	}

	// Update claude.json: read current, merge target's oauthAccount, write back.
	currentClaudeCfg, err := config.ReadClaudeConfig(s.claudeConfigPath)
	if err != nil {
		s.rollback(snap)
		return fmt.Errorf("reading claude config for merge: %w", err)
	}

	// Extract oauthAccount from target's config backup.
	if targetConfigJSON != "" {
		var targetCfgMap map[string]any
		if err := json.Unmarshal([]byte(targetConfigJSON), &targetCfgMap); err == nil {
			if oaData, ok := targetCfgMap["oauthAccount"]; ok {
				currentClaudeCfg["oauthAccount"] = oaData
			}
		}
	} else {
		// No config backup; construct oauthAccount from Account data.
		currentClaudeCfg["oauthAccount"] = map[string]any{
			"emailAddress":     targetAcct.Email,
			"accountUuid":      targetAcct.UUID,
			"organizationUuid": targetAcct.OrganizationUUID,
			"organizationName": targetAcct.OrganizationName,
		}
	}

	if err := config.WriteClaudeConfig(s.claudeConfigPath, currentClaudeCfg); err != nil {
		s.rollback(snap)
		return fmt.Errorf("writing claude config: %w", err)
	}

	// Update sequence.json.
	cfg.ActiveAccountNumber = &targetNum
	if err := s.writeSequence(cfg); err != nil {
		s.rollback(snap)
		return fmt.Errorf("writing sequence: %w", err)
	}

	return nil
}
