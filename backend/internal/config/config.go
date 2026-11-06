// Package config loads LightMail configuration from environment variables.
//
// Every secret variable can also be provided through a file by appending
// "_FILE" to its name (Docker secrets convention), e.g.
// LIGHTMAIL_MASTER_KEY_FILE=/run/secrets/lightmail_master_key.
package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Prefix is the prefix used by every environment variable.
const Prefix = "LIGHTMAIL_"

type OAuthClient struct {
	ClientID     string
	ClientSecret string
	Tenant       string // Microsoft only
}

func (c OAuthClient) Configured() bool { return c.ClientID != "" }

type Config struct {
	AppName    string
	ListenAddr string
	PublicURL  *url.URL
	DataDir    string
	DBPath     string

	MasterKey []byte
	OldKeys   [][]byte

	AdminUser     string
	AdminPassword string

	CookieSecure       bool
	SessionIdleTTL     time.Duration
	SessionAbsoluteTTL time.Duration
	TrustedProxies     []netip.Prefix

	LogLevel  slog.Level
	LogFormat string

	SyncInterval          time.Duration
	SyncIdle              bool
	SyncMaxConcurrent     int
	IMAPMaxConnsPerAcct   int
	IMAPMaxConnsTotal     int
	SyncChunkSize         int
	SyncMaxPerFolder      int
	SyncBackfillBudget    time.Duration
	SyncFlagRefreshPeriod time.Duration
	NetTimeout            time.Duration

	SMTPMaxConcurrent int
	UndoSendDelay     time.Duration

	MaxAttachmentBytes int64
	MaxUploadBytes     int64
	MaxFetchBytes      int64
	CacheMaxBytes      int64
	CacheTTL           time.Duration
	MaxAccounts        int

	BlockPrivateNetworks bool
	AllowInsecureTLS     bool // development only, refused when PublicURL is https

	Google    OAuthClient
	Microsoft OAuthClient

	MetricsToken string
	WebhookURL   string
}

// Load reads the configuration from the process environment.
func Load() (*Config, error) { return load(os.Getenv) }

func load(getenv func(string) string) (*Config, error) {
	e := env{getenv: getenv}
	c := &Config{
		AppName:    e.str("APP_NAME", "LightMail"),
		ListenAddr: e.str("LISTEN", ":8080"),
		DataDir:    e.str("DATA_DIR", "/data"),
	}
	pub := e.str("PUBLIC_URL", "http://localhost:8080")
	u, err := url.Parse(strings.TrimRight(pub, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%sPUBLIC_URL must be an absolute http(s) URL, got %q", Prefix, pub)
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("%sPUBLIC_URL must not contain a path (sub-path deployments are not supported)", Prefix)
	}
	u.Path = ""
	c.PublicURL = u

	c.DBPath = e.str("DB_PATH", filepath.Join(c.DataDir, "lightmail.db"))
	if dbURL := e.str("DATABASE_URL", ""); dbURL != "" && !strings.HasPrefix(dbURL, "sqlite:") {
		return nil, fmt.Errorf("%sDATABASE_URL: only SQLite is supported in this version (see docs/LIMITATIONS.md)", Prefix)
	} else if dbURL != "" {
		c.DBPath = strings.TrimPrefix(strings.TrimPrefix(dbURL, "sqlite://"), "sqlite:")
	}

	keyStr, err := e.secret("MASTER_KEY")
	if err != nil {
		return nil, err
	}
	if keyStr == "" {
		return nil, fmt.Errorf("%sMASTER_KEY (or %sMASTER_KEY_FILE) is required: generate one with `lightmail gen-key`", Prefix, Prefix)
	}
	if c.MasterKey, err = ParseKey(keyStr); err != nil {
		return nil, fmt.Errorf("%sMASTER_KEY: %w", Prefix, err)
	}
	oldStr, err := e.secret("MASTER_KEY_OLD")
	if err != nil {
		return nil, err
	}
	for _, k := range splitList(oldStr) {
		key, err := ParseKey(k)
		if err != nil {
			return nil, fmt.Errorf("%sMASTER_KEY_OLD: %w", Prefix, err)
		}
		c.OldKeys = append(c.OldKeys, key)
	}

	c.AdminUser = e.str("ADMIN_USER", "admin")
	if c.AdminPassword, err = e.secret("ADMIN_PASSWORD"); err != nil {
		return nil, err
	}

	c.CookieSecure = e.boolean("COOKIE_SECURE", u.Scheme == "https")
	c.SessionIdleTTL = e.duration("SESSION_IDLE_TTL", 7*24*time.Hour)
	c.SessionAbsoluteTTL = e.duration("SESSION_ABSOLUTE_TTL", 30*24*time.Hour)
	for _, p := range splitList(e.str("TRUSTED_PROXIES", "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7")) {
		pfx, err := netip.ParsePrefix(p)
		if err != nil {
			return nil, fmt.Errorf("%sTRUSTED_PROXIES: invalid CIDR %q", Prefix, p)
		}
		c.TrustedProxies = append(c.TrustedProxies, pfx)
	}

	switch strings.ToLower(e.str("LOG_LEVEL", "info")) {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "warn", "warning":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		c.LogLevel = slog.LevelInfo
	}
	c.LogFormat = strings.ToLower(e.str("LOG_FORMAT", "json"))

	c.SyncInterval = e.duration("SYNC_INTERVAL", 5*time.Minute)
	c.SyncIdle = e.boolean("SYNC_IDLE", true)
	c.SyncMaxConcurrent = e.integer("SYNC_MAX_CONCURRENT", 4)
	c.IMAPMaxConnsPerAcct = e.integer("IMAP_MAX_CONNS_PER_ACCOUNT", 2)
	c.IMAPMaxConnsTotal = e.integer("IMAP_MAX_CONNS_TOTAL", 40)
	c.SyncChunkSize = e.integer("SYNC_CHUNK_SIZE", 250)
	c.SyncMaxPerFolder = e.integer("SYNC_MAX_MESSAGES_PER_FOLDER", 0)
	c.SyncBackfillBudget = e.duration("SYNC_BACKFILL_BUDGET", 2*time.Minute)
	c.SyncFlagRefreshPeriod = e.duration("SYNC_FLAG_REFRESH", 10*time.Minute)
	c.NetTimeout = e.duration("NET_TIMEOUT", 60*time.Second)

	c.SMTPMaxConcurrent = e.integer("SMTP_MAX_CONCURRENT", 2)
	c.UndoSendDelay = e.duration("UNDO_SEND_DELAY", 5*time.Second)

	c.MaxAttachmentBytes = int64(e.integer("MAX_ATTACHMENT_MB", 25)) << 20
	c.MaxUploadBytes = int64(e.integer("MAX_MESSAGE_SIZE_MB", 35)) << 20
	c.MaxFetchBytes = int64(e.integer("MAX_FETCH_MB", 50)) << 20
	c.CacheMaxBytes = int64(e.integer("CACHE_MAX_MB", 1024)) << 20
	c.CacheTTL = e.duration("CACHE_TTL", 30*24*time.Hour)
	c.MaxAccounts = e.integer("MAX_ACCOUNTS", 20)

	c.BlockPrivateNetworks = e.boolean("BLOCK_PRIVATE_NETWORKS", false)
	c.AllowInsecureTLS = e.boolean("DEV_ALLOW_INSECURE_TLS", false)
	if c.AllowInsecureTLS && u.Scheme == "https" {
		return nil, fmt.Errorf("%sDEV_ALLOW_INSECURE_TLS cannot be enabled when PUBLIC_URL uses https (production)", Prefix)
	}

	c.Google.ClientID = e.str("GOOGLE_CLIENT_ID", "")
	if c.Google.ClientSecret, err = e.secret("GOOGLE_CLIENT_SECRET"); err != nil {
		return nil, err
	}
	c.Microsoft.ClientID = e.str("MICROSOFT_CLIENT_ID", "")
	if c.Microsoft.ClientSecret, err = e.secret("MICROSOFT_CLIENT_SECRET"); err != nil {
		return nil, err
	}
	c.Microsoft.Tenant = e.str("MICROSOFT_TENANT", "common")

	if c.MetricsToken, err = e.secret("METRICS_TOKEN"); err != nil {
		return nil, err
	}
	if c.WebhookURL, err = e.secret("WEBHOOK_URL"); err != nil {
		return nil, err
	}
	if c.WebhookURL != "" {
		wu, err := url.Parse(c.WebhookURL)
		if err != nil || (wu.Scheme != "https" && wu.Scheme != "http") {
			return nil, fmt.Errorf("%sWEBHOOK_URL must be an http(s) URL", Prefix)
		}
	}

	if e.err != nil {
		return nil, e.err
	}
	return c, nil
}

// OAuthRedirectURL returns the redirect URI registered for a provider.
func (c *Config) OAuthRedirectURL(provider string) string {
	return c.PublicURL.String() + "/api/v1/oauth/" + provider + "/callback"
}

// ParseKey accepts a 32-byte key encoded as base64 (std or URL) or hex.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, dec := range []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
		hex.DecodeString,
	} {
		if b, err := dec(s); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, errors.New("key must be 32 random bytes encoded as base64 or hex")
}

type env struct {
	getenv func(string) string
	err    error
}

func (e *env) str(name, def string) string {
	if v := strings.TrimSpace(e.getenv(Prefix + name)); v != "" {
		return v
	}
	return def
}

// secret reads NAME or the content of the file pointed by NAME_FILE.
func (e *env) secret(name string) (string, error) {
	if path := strings.TrimSpace(e.getenv(Prefix + name + "_FILE")); path != "" {
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrPermission) {
			// Compose file secrets are bind mounts that keep the host owner and
			// mode, and the container runs as an unprivileged user.
			return "", fmt.Errorf("%s%s_FILE: %w: the file must be readable by uid %d, the container user (on the host: sudo chown %d:%d <file> && sudo chmod 600 <file>)",
				Prefix, name, err, os.Getuid(), os.Getuid(), os.Getgid())
		}
		if err != nil {
			return "", fmt.Errorf("%s%s_FILE: %w", Prefix, name, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return strings.TrimSpace(e.getenv(Prefix + name)), nil
}

func (e *env) integer(name string, def int) int {
	v := e.str(name, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		e.err = errors.Join(e.err, fmt.Errorf("%s%s: invalid non-negative integer %q", Prefix, name, v))
		return def
	}
	return n
}

func (e *env) boolean(name string, def bool) bool {
	v := e.str(name, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.err = errors.Join(e.err, fmt.Errorf("%s%s: invalid boolean %q", Prefix, name, v))
		return def
	}
	return b
}

func (e *env) duration(name string, def time.Duration) time.Duration {
	v := e.str(name, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		if n, err2 := strconv.Atoi(v); err2 == nil {
			return time.Duration(n) * time.Second
		}
		e.err = errors.Join(e.err, fmt.Errorf("%s%s: invalid duration %q (e.g. 30s, 5m)", Prefix, name, v))
		return def
	}
	return d
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
