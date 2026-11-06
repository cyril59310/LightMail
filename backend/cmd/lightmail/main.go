// Command lightmail is the LightMail server and maintenance tool.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // timezone database for minimal container images

	"lightmail/internal/api"
	"lightmail/internal/app"
	"lightmail/internal/auth"
	"lightmail/internal/backup"
	"lightmail/internal/blobs"
	"lightmail/internal/config"
	"lightmail/internal/crypto"
	"lightmail/internal/logging"
	"lightmail/internal/store"
)

const usage = `LightMail — lightweight self-hosted mail client

Usage: lightmail <command> [options]

Commands:
  serve                     Run the server (default)
  healthcheck               Exit 0 if the local server is healthy (Docker HEALTHCHECK)
  gen-key                   Print a new random master encryption key
  user create <name> [--admin]
                            Create a user (password read from stdin); the first
                            user is always an administrator
  user set-password <name>  Change a user password (password read from stdin)
  user reset-2fa <name>     Disable two-factor authentication of a user
  user set-admin <name> on|off
                            Grant or remove the administrator role
  user list                 List users and their role
  backup [-out FILE] [-with-cache]
                            Write a consistent backup archive (server may be running)
  verify-backup FILE        Check archive checksums, database integrity and key
  restore FILE              Restore an archive (server must be stopped)
  rotate-key                Re-encrypt secrets with LIGHTMAIL_MASTER_KEY
                            (old key in LIGHTMAIL_MASTER_KEY_OLD; server stopped)
  migrate                   Apply database migrations and exit
  version                   Print the version
`

func main() {
	cmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "healthcheck":
		err = healthcheck()
	case "gen-key":
		err = genKey()
	case "user":
		err = userCmd(args)
	case "backup":
		err = backupCmd(args)
	case "verify-backup":
		err = verifyCmd(args)
	case "restore":
		err = restoreCmd(args)
	case "rotate-key":
		err = rotateCmd()
	case "migrate":
		err = migrateCmd()
	case "version":
		fmt.Println("lightmail", api.Version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func loadConfig() (*config.Config, *slog.Logger, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	return cfg, logging.New(os.Stdout, cfg.LogLevel, cfg.LogFormat), nil
}

func serve() error {
	cfg, log, err := loadConfig()
	if err != nil {
		return err
	}
	unlock, err := backup.LockDataDir(cfg.DataDir)
	if err != nil {
		if mkErr := os.MkdirAll(cfg.DataDir, 0o700); mkErr == nil {
			unlock, err = backup.LockDataDir(cfg.DataDir)
		}
		if err != nil {
			return err
		}
	}
	defer unlock()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	a, err := app.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	if err := a.Start(ctx); err != nil {
		a.Stop()
		return err
	}
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           a.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute, // large attachment uploads
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		a.Stop()
		return err
	}
	log.Info("LightMail started", "version", api.Version, "listen", cfg.ListenAddr, "public_url", cfg.PublicURL.String(), "data_dir", cfg.DataDir, "key_id", a.KR.PrimaryID())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server failed", "err", err)
		}
	}
	log.Info("shutting down")
	shCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(shCtx)
	a.Stop()
	log.Info("stopped")
	return nil
}

func healthcheck() error {
	addr := os.Getenv("LIGHTMAIL_LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	c := &http.Client{Timeout: 4 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + "/api/v1/health")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: HTTP %d", resp.StatusCode)
	}
	return nil
}

func genKey() error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	fmt.Println(base64.StdEncoding.EncodeToString(b))
	return nil
}

func readPassword() (string, error) {
	if p := os.Getenv("LIGHTMAIL_NEW_PASSWORD"); p != "" {
		return p, nil
	}
	fmt.Fprint(os.Stderr, "Password: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func openDB(cfg *config.Config) (*store.DB, error) {
	return store.Open(context.Background(), cfg.DBPath, 1)
}

const userUsage = "usage: lightmail user create <username> [--admin] | set-password <username> | set-admin <username> on|off | reset-2fa <username> | list"

func userCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(userUsage)
	}
	sub, rest := args[0], args[1:]
	makeAdmin := false
	if sub == "create" && len(rest) == 2 && (rest[1] == "--admin" || rest[1] == "-admin") {
		makeAdmin, rest = true, rest[:1]
	}
	switch {
	case sub == "list" && len(rest) == 0:
	case sub == "set-admin" && len(rest) == 2 && (rest[1] == "on" || rest[1] == "off"):
	case (sub == "create" || sub == "set-password" || sub == "reset-2fa") && len(rest) == 1:
	default:
		return errors.New(userUsage)
	}
	cfg, _, err := loadConfig()
	if err != nil {
		return err
	}
	var hash string
	if sub == "create" || sub == "set-password" {
		pw, err := readPassword()
		if err != nil {
			return err
		}
		if hash, err = auth.HashPassword(pw); err != nil {
			return err
		}
	}
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()

	switch sub {
	case "list":
		users, err := db.ListUsers(ctx)
		if err != nil {
			return err
		}
		for _, u := range users {
			role, tfa := "user", ""
			if u.IsAdmin {
				role = "admin"
			}
			if u.TOTPEnabled {
				tfa = "  2fa"
			}
			fmt.Printf("%-30s %-5s%s\n", u.Username, role, tfa)
		}
		return nil
	case "create":
		// The very first user administers the instance.
		if n, err := db.CountUsers(ctx); err == nil && n == 0 {
			makeAdmin = true
		}
		if _, err := db.CreateUser(ctx, rest[0], hash, makeAdmin); err != nil {
			return err
		}
		role := "user"
		if makeAdmin {
			role = "administrator"
		}
		fmt.Printf("user created: %s (%s)\n", rest[0], role)
		return nil
	}

	u, err := db.UserByName(ctx, rest[0])
	if err != nil {
		return fmt.Errorf("user %q: %w", rest[0], err)
	}
	switch sub {
	case "set-admin":
		if err := db.SetUserAdmin(ctx, u.ID, rest[1] == "on"); err != nil {
			return err
		}
		fmt.Printf("%s is now %s\n", u.Username, map[bool]string{true: "an administrator", false: "a regular user"}[rest[1] == "on"])
	case "reset-2fa":
		if err := db.DisableTOTP(ctx, u.ID); err != nil {
			return err
		}
		_ = db.DeleteUserSessions(ctx, u.ID, "")
		fmt.Println("two-factor authentication disabled, all sessions revoked:", u.Username)
	case "set-password":
		if err := db.SetUserPassword(ctx, u.ID, hash); err != nil {
			return err
		}
		_ = db.DeleteUserSessions(ctx, u.ID, "")
		fmt.Println("password changed, all sessions revoked")
	}
	return nil
}

func backupCmd(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	out := fs.String("out", "", "output file (default: <data>/backups/lightmail-<date>.tar.gz)")
	withCache := fs.Bool("with-cache", false, "include the attachment cache")
	fs.Parse(args)
	cfg, _, err := loadConfig()
	if err != nil {
		return err
	}
	if *out == "" {
		*out = filepath.Join(cfg.DataDir, "backups", "lightmail-"+time.Now().UTC().Format("20060102-150405")+".tar.gz")
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o700); err != nil {
		return err
	}
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	bs, err := blobs.New(filepath.Join(cfg.DataDir, "blobs"))
	if err != nil {
		return err
	}
	m, err := backup.Create(context.Background(), db, bs, crypto.KeyID(cfg.MasterKey), api.Version, *out, *withCache)
	if err != nil {
		return err
	}
	fmt.Printf("backup written: %s (%d files, key %s)\n", *out, len(m.Files), m.KeyID)
	fmt.Println("reminder: the master key is not in the archive; keep it in a separate safe place.")
	return nil
}

func keyringFromEnv() (*crypto.Keyring, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return crypto.NewKeyring(cfg.MasterKey, cfg.OldKeys...)
}

func verifyCmd(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: lightmail verify-backup FILE")
	}
	kr, err := keyringFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: no usable master key, the key check is skipped:", err)
	}
	rep, err := backup.Verify(context.Background(), args[0], kr)
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(b))
	if !rep.OK() {
		return errors.New("backup verification FAILED")
	}
	fmt.Println("backup OK")
	return nil
}

func restoreCmd(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: lightmail restore FILE")
	}
	cfg, _, err := loadConfig()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	unlock, err := backup.LockDataDir(cfg.DataDir)
	if err != nil {
		return err
	}
	defer unlock()
	kr, err := crypto.NewKeyring(cfg.MasterKey, cfg.OldKeys...)
	if err != nil {
		return err
	}
	rep, err := backup.Restore(context.Background(), args[0], cfg.DBPath, filepath.Join(cfg.DataDir, "blobs"), kr)
	if err != nil {
		if rep != nil {
			b, _ := json.MarshalIndent(rep, "", "  ")
			fmt.Fprintln(os.Stderr, string(b))
		}
		return err
	}
	fmt.Printf("restored backup from %s (%d accounts, %d cached messages). Start LightMail to resume synchronisation.\n", rep.Manifest.CreatedAt.Format(time.RFC3339), rep.Accounts, rep.Messages)
	return nil
}

func rotateCmd() error {
	cfg, _, err := loadConfig()
	if err != nil {
		return err
	}
	if len(cfg.OldKeys) == 0 {
		return errors.New("set the previous key in LIGHTMAIL_MASTER_KEY_OLD and the new one in LIGHTMAIL_MASTER_KEY")
	}
	unlock, err := backup.LockDataDir(cfg.DataDir)
	if err != nil {
		return err
	}
	defer unlock()
	kr, err := crypto.NewKeyring(cfg.MasterKey, cfg.OldKeys...)
	if err != nil {
		return err
	}
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.CheckKey(context.Background(), kr); err != nil {
		return err
	}
	n, err := db.RotateKeys(context.Background(), kr)
	if err != nil {
		return err
	}
	fmt.Printf("%d secrets re-encrypted with key %s. You can now remove LIGHTMAIL_MASTER_KEY_OLD.\n", n, kr.PrimaryID())
	return nil
}

func migrateCmd() error {
	cfg, _, err := loadConfig()
	if err != nil {
		return err
	}
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	v, err := db.SchemaVersion(context.Background())
	if err != nil {
		return err
	}
	fmt.Println("database schema version", v)
	return nil
}
