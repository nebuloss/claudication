// Command claudication is a multi-provider LLM gateway.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/nebuloss/claudication/internal/config"
	"github.com/nebuloss/claudication/internal/httpapi"
	"github.com/nebuloss/claudication/internal/logging"
	"github.com/nebuloss/claudication/internal/secret"
	"github.com/nebuloss/claudication/internal/store"
	"github.com/nebuloss/claudication/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "claudication: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("no command given")
	}

	switch args[0] {
	case "serve":
		return cmdServe(args[1:])
	case "keys":
		return cmdKeys(args[1:])
	case "login-url":
		return cmdLoginURL(args[1:])
	case "passwd":
		return cmdPasswd(args[1:])
	case "version", "--version", "-v":
		fmt.Println(version.String())
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `claudication - multi-provider LLM gateway

Usage:
  claudication serve [-config FILE]        Run the gateway
  claudication passwd                      Set the admin password
  claudication login-url                   Mint a single-use UI sign-in link
  claudication keys add -name NAME         Mint a client API key
  claudication keys list                   List API keys
  claudication keys revoke -id ID          Revoke an API key
  claudication version                     Print build information

Environment:
  CLAUDICATION_LISTEN, CLAUDICATION_STATE_DIR, CLAUDICATION_LOG_LEVEL, CLAUDICATION_LOG_FORMAT,
  CLAUDICATION_REQUESTS_PER_MINUTE, CLAUDICATION_SECRET_KEY

The admin UI is served at / once the gateway is running. On a fresh
install it asks for a password. If that password is lost, "claudication
passwd" is the way back in: shell access to the state directory is already the
higher privilege.
`)
}

// loginURL renders the sign-in link around a freshly minted token.
//
// The bind address is not always a usable hostname: 0.0.0.0 and :: mean "every
// interface", which no browser can resolve, so fall back to loopback and let
// the operator substitute the real host.
func loginURL(listen, token string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		host, port = "127.0.0.1", "8317"
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%s/?token=%s", host, port, token)
}

// openState loads config and opens the database, the two steps every command
// shares.
func openState(ctx context.Context, configPath string) (config.Config, *store.Store, *secret.Sealer, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return config.Config{}, nil, nil, err
	}
	if err := cfg.EnsureStateDir(); err != nil {
		return config.Config{}, nil, nil, err
	}
	sealer, err := secret.Load(cfg.StateDir)
	if err != nil {
		return config.Config{}, nil, nil, err
	}
	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return config.Config{}, nil, nil, err
	}
	return cfg, st, sealer, nil
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config.yaml (optional)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Both signals drain the same way. SIGTERM is what containers and
	// systemd actually send.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, st, sealer, err := openState(ctx, *configPath)
	if err != nil {
		return err
	}
	defer st.Close()

	log := logging.New(cfg.Log.Level, cfg.Log.Format)

	srv, err := httpapi.New(cfg, log, st, sealer)
	if err != nil {
		return err
	}
	return srv.Run(ctx)
}

// cmdLoginURL mints a fresh single-use sign-in link.
//
// It exists so an operator on the box can reach the UI without typing a
// password into a shared machine, and so a fresh container can hand out one
// way in. The link is a short-lived handle rather than an encoded credential,
// so leaving one in shell history costs a link, not the account.
func cmdLoginURL(args []string) error {
	fs := flag.NewFlagSet("login-url", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config.yaml (optional)")
	ttl := fs.Duration("ttl", store.LoginLinkTTL, "how long the link stays valid")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cfg, st, _, err := openState(ctx, *configPath)
	if err != nil {
		return err
	}
	defer st.Close()

	token, expires, err := st.MintLoginLink(ctx, *ttl)
	if errors.Is(err, store.ErrNoAdmin) {
		return errors.New("no admin password set yet; set one with: claudication passwd")
	}
	if err != nil {
		return err
	}
	fmt.Println(loginURL(cfg.Listen, token))
	fmt.Fprintf(os.Stderr, "single use, valid until %s\n", expires.Local().Format(time.RFC1123))
	return nil
}

// cmdPasswd sets the admin password from the shell.
//
// This is both first-run setup and the recovery path. It does not ask for the
// old password: whoever can run this can already read the database it protects,
// so demanding the forgotten password would lock out the one person the
// account belongs to while stopping nobody.
func cmdPasswd(args []string) error {
	fs := flag.NewFlagSet("passwd", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config.yaml (optional)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, st, _, err := openState(ctx, *configPath)
	if err != nil {
		return err
	}
	defer st.Close()

	existed, err := st.AdminExists(ctx)
	if err != nil {
		return err
	}

	password, err := readPassword("New admin password: ")
	if err != nil {
		return err
	}
	// The confirmation guards against a typo nobody can see. A pipe has no
	// typos to catch and no second line to read, so asking would just fail:
	// `claudication passwd < secret` is how provisioning sets this.
	if term.IsTerminal(int(os.Stdin.Fd())) {
		again, err := readPassword("Repeat: ")
		if err != nil {
			return err
		}
		if password != again {
			return errors.New("the two passwords did not match")
		}
	}
	if err := st.SetAdminPassword(ctx, password); err != nil {
		return err
	}

	if existed {
		fmt.Println("Password changed. Sessions opened with the old one are not " +
			"ended until the gateway restarts.")
	} else {
		fmt.Println("Admin password set. Sign in at the gateway's address.")
	}
	return nil
}

// readPassword reads without echoing when stdin is a terminal, and plainly
// when it is not, so `claudication passwd < secret` works in a provisioning
// script. The prompt goes to stderr either way, keeping stdout clean.
func readPassword(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read password: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}

	fmt.Fprint(os.Stderr, prompt)
	raw, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(raw), nil
}

func cmdKeys(args []string) error {
	if len(args) == 0 {
		return errors.New("keys: expected add, list or revoke")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("keys add", flag.ContinueOnError)
		configPath := fs.String("config", "", "path to config.yaml (optional)")
		name := fs.String("name", "", "human-readable name for the key (required)")
		rpm := fs.Int("rpm", 0, "per-key requests per minute (0 = use the global default)")
		budget := fs.Int64("token-budget", 0, "token budget (0 = unlimited)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" {
			return errors.New("keys add: -name is required")
		}

		_, st, _, err := openState(ctx, *configPath)
		if err != nil {
			return err
		}
		defer st.Close()

		key, plaintext, err := st.CreateKey(ctx, *name, *rpm, *budget)
		if err != nil {
			return err
		}
		fmt.Printf("Created API key %q (id %s)\n\n  %s\n\n", key.Name, key.ID, plaintext)
		fmt.Println("This is the only time the key is shown. Store it now.")
		return nil

	case "list":
		fs := flag.NewFlagSet("keys list", flag.ContinueOnError)
		configPath := fs.String("config", "", "path to config.yaml (optional)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		_, st, _, err := openState(ctx, *configPath)
		if err != nil {
			return err
		}
		defer st.Close()

		keys, err := st.ListKeys(ctx)
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			fmt.Println("No API keys. Create one with: claudication keys add -name NAME")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tKEY\tCREATED\tLAST USED\tRPM\tSTATUS")
		for _, k := range keys {
			status := "active"
			if k.Revoked() {
				status = "revoked"
			}
			last := "never"
			if k.LastUsedAt != nil {
				last = k.LastUsedAt.Format(time.RFC3339)
			}
			rpm := "default"
			if k.RPMLimit > 0 {
				rpm = fmt.Sprint(k.RPMLimit)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				k.ID, k.Name, k.Display(), k.CreatedAt.Format(time.RFC3339), last, rpm, status)
		}
		return w.Flush()

	case "revoke":
		fs := flag.NewFlagSet("keys revoke", flag.ContinueOnError)
		configPath := fs.String("config", "", "path to config.yaml (optional)")
		id := fs.String("id", "", "key id to revoke (required)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return errors.New("keys revoke: -id is required")
		}
		_, st, _, err := openState(ctx, *configPath)
		if err != nil {
			return err
		}
		defer st.Close()

		if err := st.RevokeKey(ctx, *id); err != nil {
			return err
		}
		fmt.Printf("Revoked key %s\n", *id)
		return nil

	default:
		return fmt.Errorf("keys: unknown subcommand %q", args[0])
	}
}
