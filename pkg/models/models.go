package models

// SwapConfig represents the contents of sequence.json, which tracks
// configured accounts and the active account selection.
type SwapConfig struct {
	ActiveAccountNumber *int               `json:"activeAccountNumber"`
	Sequence            []int              `json:"sequence"`
	Accounts            map[string]Account `json:"accounts"`
	LastUpdated         string             `json:"lastUpdated"`
}

// Account represents a single Claude account entry stored in the swap config.
type Account struct {
	Email            string `json:"email"`
	UUID             string `json:"uuid"`
	OrganizationUUID string `json:"organizationUuid"`
	OrganizationName string `json:"organizationName"`
	Added            string `json:"added"`
}

// OAuthAccount represents the oauthAccount field from ~/.claude.json.
type OAuthAccount struct {
	EmailAddress     string `json:"emailAddress"`
	AccountUUID      string `json:"accountUuid"`
	OrganizationUUID string `json:"organizationUuid"`
	OrganizationName string `json:"organizationName"`
	OrganizationRole string `json:"organizationRole"`
	DisplayName      string `json:"displayName"`
}

// Credentials represents a credential JSON file containing OAuth data.
type Credentials struct {
	ClaudeAIOAuth *OAuthData `json:"claudeAiOauth"`
}

// OAuthData holds the OAuth token details within a credential file.
type OAuthData struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken"`
	ExpiresAt    int64    `json:"expiresAt"`
	Scopes       []string `json:"scopes"`
}

// UsageResponse represents the response from the Claude usage API.
type UsageResponse struct {
	FiveHour *UsageWindow `json:"five_hour"`
	SevenDay *UsageWindow `json:"seven_day"`
}

// UsageWindow represents a single usage time window with utilization and reset info.
type UsageWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}
