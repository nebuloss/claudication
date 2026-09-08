// Command claudication is a gateway that proxies the Anthropic Messages API to
// Claude subscription accounts.
//
// "Multi-provider" is the design, not yet the product: the translation lane
// that would make it true is unbuilt, so nothing user-facing claims it.
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

	"claudication/internal/config"
	"claudication/internal/httpapi"
	"claudication/internal/logging"
	"claudication/internal/secret"
	"claudication/internal/store"
	"claudication/internal/version"
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
	case "vacuum":
		return cmdVacuum(args[1:])
	case "backup":
		return cmdBackup(args[1:])
	case "restore":
		return cmdRestore(args[1:])
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
	fmt.Fprint(os.Stderr, `claudication - Claude API gateway

Usage:
  claudication serve [-config FILE]        Run the gateway
  claudication passwd                      Set the admin password
  claudication login-url                   Mint a single-use UI sign-in link
  claudication keys add -name NAME         Mint a client API key
  claudication keys list                   List API keys
  claudication keys delete -id ID          Withdraw an API key
  claudication backup [-out FILE]          Snapshot the state to one file
  claudication restore -in FILE            Put a backup back
  claudication vacuum                      Compact the database, reclaiming disk
  claudication version                     Print build information

Environment:
  CLAUDICATION_LISTEN, CLAUDICATION_STATE_DIR, CLAUDICATION_LOG_LEVEL, CLAUDICATION_LOG_FORMAT,
  CLAUDICATION_REQUESTS_PER_MINUTE, CLAUDICATION_SECRET_KEY,
  CLAUDICATION_CLAUDE_CODE_ATTRIBUTION, CLAUDICATION_TRUSTED_PROXIES

Configuration is optional. With no -config, /etc/claudication/config.yaml is
read if it exists; environment variables override either.

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

// defaultConfigPath is read when -config is not given and it exists.
//
// Without this the config file was a trap: the installed service runs `serve`
// with no -config, and Load reads nothing when the path is empty — so an
// operator who followed config.example.yaml and dropped it at the obvious place
// got no effect, no warning, and nothing in the log to say why the setting they
// had just written was being ignored.
const defaultConfigPath = "/etc/claudication/config.yaml"

// resolveConfigPath returns the explicit path, or the default when one is
// there. A missing default is not an error — most installs have no config file
// at all and should not be made to create one.
func resolveConfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if _, err := os.Stat(defaultConfigPath); err == nil {
		return defaultConfigPath
	}
	return ""
}

// openState loads config and opens the database, the two steps every command
// shares.
func openState(ctx context.Context, configPath string) (config.Config, *store.Store, *secret.Sealer, error) {
	cfg, err := config.Load(resolveConfigPath(configPath))
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

// cmdVacuum compacts the database and gives the freed space back.
//
// Pruning old usage events marks pages reusable but does not shrink the file,
// so a gateway that has been busy — or one whose retention-days was lowered to
// recover space — sits at its historical peak indefinitely. A database created
// with auto_vacuum reclaims space by itself after each daily prune; this is the
// one-off for a file created before that, and it is what converts it so the
// automatic path works from then on.
func cmdVacuum(args []string) error {
	fs := flag.NewFlagSet("vacuum", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config.yaml (optional)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// No deadline worth setting: this rewrites the whole file, and how long
	// that takes is a property of the disk. Interrupting it is safe — VACUUM
	// is a transaction — but pointless.
	ctx := context.Background()

	_, st, _, err := openState(ctx, *configPath)
	if err != nil {
		return err
	}
	defer st.Close()

	mode, err := st.AutoVacuum(ctx)
	if err != nil {
		return err
	}
	if mode == 0 {
		fmt.Println("This database predates automatic reclamation; converting it.")
	}

	fmt.Println("Compacting. The gateway should be stopped, and this needs about")
	fmt.Println("twice the database's size free while it runs.")

	before, after, err := st.Vacuum(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("\n  before  %s\n  after   %s\n", humanBytes(before), humanBytes(after))
	switch {
	case after < before:
		fmt.Printf("  freed   %s\n", humanBytes(before-after))
	case after > before:
		// Converting to auto_vacuum adds pointer-map pages, which is what makes
		// future prunes able to give space back. On a database with little to
		// reclaim that shows up as a small increase, and reporting it as
		// "already compact" would be describing the opposite of what happened.
		fmt.Printf("  grew    %s, adding the bookkeeping that lets it shrink later\n",
			humanBytes(after-before))
	default:
		fmt.Println("  freed   nothing; it was already compact")
	}
	if mode == 0 {
		fmt.Println("\nFrom now on the daily prune reclaims space on its own.")
	}
	return nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
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
		return errors.New("keys: expected add, list or delete")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("keys add", flag.ContinueOnError)
		configPath := fs.String("config", "", "path to config.yaml (optional)")
		name := fs.String("name", "", "human-readable name for the key (required)")
		rpm := fs.Int("rpm", 0, "per-key requests per minute (0 = use the global default)")
		budget := fs.Int64("token-budget", 0,
			"tokens this key may spend per rolling 24 hours (0 = unlimited)")
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
		fmt.Fprintln(w, "ID\tNAME\tKEY\tCREATED\tLAST USED\tRPM\tTOKENS/DAY")
		for _, k := range keys {
			last := "never"
			if k.LastUsedAt != nil {
				last = k.LastUsedAt.Format(time.RFC3339)
			}
			rpm := "default"
			if k.RPMLimit > 0 {
				rpm = fmt.Sprint(k.RPMLimit)
			}
			// Spelled out rather than shown as 0, which reads as "none allowed"
			// where it means the opposite.
			budget := "unlimited"
			if k.TokenBudget > 0 {
				budget = fmt.Sprint(k.TokenBudget)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				k.ID, k.Name, k.Display(), k.CreatedAt.Format(time.RFC3339), last, rpm, budget)
		}
		return w.Flush()

	case "delete":
		fs := flag.NewFlagSet("keys delete", flag.ContinueOnError)
		configPath := fs.String("config", "", "path to config.yaml (optional)")
		id := fs.String("id", "", "key id to delete (required)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return errors.New("keys delete: -id is required")
		}
		_, st, _, err := openState(ctx, *configPath)
		if err != nil {
			return err
		}
		defer st.Close()

		if err := st.DeleteKey(ctx, *id); err != nil {
			return err
		}
		fmt.Printf("Deleted key %s\n", *id)
		return nil

	default:
		return fmt.Errorf("keys: unknown subcommand %q", args[0])
	}
}
