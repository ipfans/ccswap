package switcher

import (
	"fmt"
	"strconv"

	"github.com/ipfans/ccswap/pkg/lock"
	"github.com/ipfans/ccswap/pkg/oauth"
)

// AutoSwitch checks the active account's usage against the given threshold
// percentage (e.g., 80 means 80%). If either usage window exceeds the
// threshold, it iterates through the remaining accounts in sequence order
// looking for a healthy candidate and switches to it.
func (s *Switcher) AutoSwitch(threshold float64) error {
	// Acquire file lock.
	unlock, err := lock.Acquire(s.lockFile, lock.DefaultTimeout)
	if err != nil {
		return fmt.Errorf("acquiring lock: %w", err)
	}

	// Read sequence.json.
	cfg, err := s.readSequence()
	if err != nil {
		unlock()
		return err
	}

	if cfg.ActiveAccountNumber == nil {
		unlock()
		return fmt.Errorf("no active account configured")
	}

	activeNum := *cfg.ActiveAccountNumber
	activeKey := strconv.Itoa(activeNum)
	activeAcct, ok := cfg.Accounts[activeKey]
	if !ok {
		unlock()
		return fmt.Errorf("active account #%d not found in accounts", activeNum)
	}

	// Read active account credentials.
	credsJSON, err := s.store.ReadAccountCredentials(activeNum, activeAcct.Email)
	if err != nil {
		unlock()
		return fmt.Errorf("reading credentials for account #%d: %w", activeNum, err)
	}
	if credsJSON == "" {
		unlock()
		return fmt.Errorf("no credentials for active account #%d (%s)", activeNum, activeAcct.Email)
	}

	// Fetch live usage for the active account (useCache=false for auto decisions).
	persistFn := func(updated string) error {
		return s.store.WriteAccountCredentials(activeNum, activeAcct.Email, updated)
	}
	usage, err := oauth.FetchUsageWithCache(
		s.usageCache, false, activeNum, activeAcct.Email,
		credsJSON, true, persistFn, s.httpClient,
	)
	if err != nil {
		unlock()
		return fmt.Errorf("fetching usage for account #%d (%s): %w", activeNum, activeAcct.Email, err)
	}

	// Check if current account is healthy.
	thresholdFraction := threshold / 100.0
	fiveH := 0.0
	sevenD := 0.0
	if usage.FiveHour != nil {
		fiveH = usage.FiveHour.Utilization
	}
	if usage.SevenDay != nil {
		sevenD = usage.SevenDay.Utilization
	}

	if fiveH < thresholdFraction && sevenD < thresholdFraction {
		unlock()
		fmt.Printf("✓ Current account #%d (%s) is healthy (5h: %.1f%%, 7d: %.1f%%)\n",
			activeNum, activeAcct.Email, fiveH*100, sevenD*100)
		return nil
	}

	// Current account exceeds threshold; search for a healthy candidate.
	var candidateNum int
	var candidateEmail string
	var candidateFiveH, candidateSevenD float64
	found := false

	for _, num := range cfg.Sequence {
		if num == activeNum {
			continue
		}

		key := strconv.Itoa(num)
		acct, ok := cfg.Accounts[key]
		if !ok {
			continue
		}

		candCreds, err := s.store.ReadAccountCredentials(num, acct.Email)
		if err != nil || candCreds == "" {
			continue
		}

		candPersistFn := func(updated string) error {
			return s.store.WriteAccountCredentials(num, acct.Email, updated)
		}
		candUsage, err := oauth.FetchUsageForAccount(
			num, acct.Email, candCreds, false, candPersistFn, s.httpClient,
		)
		if err != nil {
			// Usage API failed for this candidate; skip and try next.
			continue
		}

		candFiveH := 0.0
		candSevenD := 0.0
		if candUsage.FiveHour != nil {
			candFiveH = candUsage.FiveHour.Utilization
		}
		if candUsage.SevenDay != nil {
			candSevenD = candUsage.SevenDay.Utilization
		}

		if candFiveH < thresholdFraction && candSevenD < thresholdFraction {
			candidateNum = num
			candidateEmail = acct.Email
			candidateFiveH = candFiveH
			candidateSevenD = candSevenD
			found = true
			break
		}
	}

	if !found {
		unlock()
		return fmt.Errorf("no healthy account available (all accounts exceed %.0f%% threshold)", threshold)
	}

	// Release lock before performSwitch, which acquires its own lock.
	unlock()

	fmt.Printf("→ Switching from #%d (%s) to #%d (%s) (5h: %.1f%%, 7d: %.1f%%)\n",
		activeNum, activeAcct.Email, candidateNum, candidateEmail,
		candidateFiveH*100, candidateSevenD*100)

	return s.performSwitch(candidateNum)
}
