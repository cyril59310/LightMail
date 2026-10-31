// Package providers describes known mail providers: server settings,
// supported authentication methods and OAuth2 endpoints.
//
// Adding a provider only requires a new entry in the registry below; OAuth2
// providers additionally need client credentials in the configuration.
package providers

import (
	"strings"
)

const (
	AuthPassword = "password"
	AuthOAuth2   = "oauth2"

	SecurityTLS      = "tls"      // implicit TLS (IMAPS 993, SMTPS 465)
	SecuritySTARTTLS = "starttls" // upgrade plaintext connection, mandatory
)

type Endpoint struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
}

type OAuthSpec struct {
	AuthURL   string            `json:"-"`
	TokenURL  string            `json:"-"`
	RevokeURL string            `json:"-"`
	Scopes    []string          `json:"scopes"`
	AuthQuery map[string]string `json:"-"`
	// Microsoft endpoints contain the tenant.
	TenantTemplate bool `json:"-"`
}

type Provider struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Domains     []string   `json:"domains,omitempty"`
	IMAP        Endpoint   `json:"imap"`
	SMTP        Endpoint   `json:"smtp"`
	AuthMethods []string   `json:"authMethods"`
	OAuth       *OAuthSpec `json:"oauth,omitempty"`
	// SMTPSavesSent: the provider stores submitted messages in the Sent
	// folder itself (appending a copy would create duplicates).
	SMTPSavesSent bool `json:"smtpSavesSent"`
	// PasswordHint explains which password to use (e.g. app password).
	PasswordHint string `json:"passwordHint,omitempty"`
	HelpURL      string `json:"helpUrl,omitempty"`
	// Folders that duplicate other folders and are not synced by default.
	SkipRoles []string `json:"-"`
}

const microsoftConsumerTenant = "9188040d-6c67-4c5b-b112-36a304b66dad"

var registry = []*Provider{
	{
		ID: "google", Name: "Gmail / Google Workspace",
		Domains:       []string{"gmail.com", "googlemail.com"},
		IMAP:          Endpoint{"imap.gmail.com", 993, SecurityTLS},
		SMTP:          Endpoint{"smtp.gmail.com", 465, SecurityTLS},
		AuthMethods:   []string{AuthOAuth2, AuthPassword},
		SMTPSavesSent: true,
		PasswordHint:  "app_password_google",
		HelpURL:       "https://support.google.com/mail/answer/185833",
		OAuth: &OAuthSpec{
			AuthURL:   "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:  "https://oauth2.googleapis.com/token",
			RevokeURL: "https://oauth2.googleapis.com/revoke",
			// https://mail.google.com/ is the only scope granting IMAP/SMTP.
			Scopes:    []string{"https://mail.google.com/", "openid", "email"},
			AuthQuery: map[string]string{"access_type": "offline", "prompt": "consent"},
		},
		SkipRoles: []string{"all", "flagged", "important"},
	},
	{
		ID: "microsoft", Name: "Outlook.com / Microsoft 365",
		Domains:       []string{"outlook.com", "outlook.fr", "hotmail.com", "hotmail.fr", "live.com", "live.fr", "msn.com"},
		IMAP:          Endpoint{"outlook.office365.com", 993, SecurityTLS},
		SMTP:          Endpoint{"smtp.office365.com", 587, SecuritySTARTTLS},
		AuthMethods:   []string{AuthOAuth2},
		SMTPSavesSent: true,
		HelpURL:       "https://learn.microsoft.com/exchange/client-developer/legacy-protocols/how-to-authenticate-an-imap-pop-smtp-application-by-using-oauth",
		OAuth: &OAuthSpec{
			AuthURL:  "https://login.microsoftonline.com/{tenant}/oauth2/v2.0/authorize",
			TokenURL: "https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token",
			Scopes: []string{
				"offline_access", "openid", "email", "profile",
				"https://outlook.office.com/IMAP.AccessAsUser.All",
				"https://outlook.office.com/SMTP.Send",
			},
			AuthQuery:      map[string]string{"prompt": "select_account"},
			TenantTemplate: true,
		},
	},
	{
		ID: "yahoo", Name: "Yahoo Mail",
		Domains:      []string{"yahoo.com", "yahoo.fr", "ymail.com"},
		IMAP:         Endpoint{"imap.mail.yahoo.com", 993, SecurityTLS},
		SMTP:         Endpoint{"smtp.mail.yahoo.com", 465, SecurityTLS},
		AuthMethods:  []string{AuthPassword},
		PasswordHint: "app_password_yahoo",
		HelpURL:      "https://help.yahoo.com/kb/SLN15241.html",
	},
	{
		ID: "icloud", Name: "iCloud Mail",
		Domains:      []string{"icloud.com", "me.com", "mac.com"},
		IMAP:         Endpoint{"imap.mail.me.com", 993, SecurityTLS},
		SMTP:         Endpoint{"smtp.mail.me.com", 587, SecuritySTARTTLS},
		AuthMethods:  []string{AuthPassword},
		PasswordHint: "app_password_icloud",
		HelpURL:      "https://support.apple.com/102525",
	},
	{
		ID: "zoho", Name: "Zoho Mail",
		Domains:      []string{"zoho.com", "zohomail.com"},
		IMAP:         Endpoint{"imap.zoho.com", 993, SecurityTLS},
		SMTP:         Endpoint{"smtp.zoho.com", 465, SecurityTLS},
		AuthMethods:  []string{AuthPassword},
		PasswordHint: "app_password_zoho",
		HelpURL:      "https://www.zoho.com/mail/help/imap-access.html",
	},
	{
		ID: "fastmail", Name: "Fastmail",
		Domains:      []string{"fastmail.com", "fastmail.fm"},
		IMAP:         Endpoint{"imap.fastmail.com", 993, SecurityTLS},
		SMTP:         Endpoint{"smtp.fastmail.com", 465, SecurityTLS},
		AuthMethods:  []string{AuthPassword},
		PasswordHint: "app_password_fastmail",
		HelpURL:      "https://www.fastmail.help/hc/en-us/articles/1500000278342",
	},
	{
		ID: "orange", Name: "Orange",
		Domains:     []string{"orange.fr", "wanadoo.fr"},
		IMAP:        Endpoint{"imap.orange.fr", 993, SecurityTLS},
		SMTP:        Endpoint{"smtp.orange.fr", 465, SecurityTLS},
		AuthMethods: []string{AuthPassword},
	},
	{
		ID: "free", Name: "Free",
		Domains:     []string{"free.fr"},
		IMAP:        Endpoint{"imap.free.fr", 993, SecurityTLS},
		SMTP:        Endpoint{"smtp.free.fr", 465, SecurityTLS},
		AuthMethods: []string{AuthPassword},
	},
	{
		ID: "laposte", Name: "La Poste",
		Domains:     []string{"laposte.net"},
		IMAP:        Endpoint{"imap.laposte.net", 993, SecurityTLS},
		SMTP:        Endpoint{"smtp.laposte.net", 465, SecurityTLS},
		AuthMethods: []string{AuthPassword},
	},
	{
		ID: "proton", Name: "Proton Mail (via Proton Mail Bridge)",
		Domains:      []string{"proton.me", "protonmail.com", "pm.me"},
		IMAP:         Endpoint{"127.0.0.1", 1143, SecuritySTARTTLS},
		SMTP:         Endpoint{"127.0.0.1", 1025, SecuritySTARTTLS},
		AuthMethods:  []string{AuthPassword},
		PasswordHint: "bridge_password",
		HelpURL:      "https://proton.me/mail/bridge",
	},
	{
		ID: "custom", Name: "IMAP / SMTP",
		IMAP:        Endpoint{"", 993, SecurityTLS},
		SMTP:        Endpoint{"", 465, SecurityTLS},
		AuthMethods: []string{AuthPassword},
	},
}

// All returns every provider.
func All() []*Provider { return registry }

// Get returns a provider by ID.
func Get(id string) *Provider {
	for _, p := range registry {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// Detect guesses the provider from an email address domain.
func Detect(email string) *Provider {
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return Get("custom")
	}
	domain := strings.ToLower(strings.TrimSpace(email[at+1:]))
	for _, p := range registry {
		for _, d := range p.Domains {
			if d == domain {
				return p
			}
		}
	}
	return Get("custom")
}

// SupportsAuth reports whether the provider accepts the method.
func (p *Provider) SupportsAuth(method string) bool {
	for _, m := range p.AuthMethods {
		if m == method {
			return true
		}
	}
	return false
}

// Endpoints returns the OAuth endpoints for a tenant (Microsoft).
func (s *OAuthSpec) Endpoints(tenant string) (auth, token string) {
	if !s.TenantTemplate {
		return s.AuthURL, s.TokenURL
	}
	if tenant == "" {
		tenant = "common"
	}
	return strings.ReplaceAll(s.AuthURL, "{tenant}", tenant), strings.ReplaceAll(s.TokenURL, "{tenant}", tenant)
}

// MicrosoftSMTPHost returns the submission host for a Microsoft tenant ID
// found in the ID token: personal accounts use smtp-mail.outlook.com.
func MicrosoftSMTPHost(tid string) string {
	if tid == microsoftConsumerTenant {
		return "smtp-mail.outlook.com"
	}
	return "smtp.office365.com"
}
