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
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"claudication/internal/config"
	"claudication/internal/secret"
	"claudication/internal/service/clients"
	"claudication/internal/store"
	"claudication/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "claudication: "+err.Error())
		os.Exit(exitCode(err))
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
	case "clients":
		return cmdClients(args[1:])
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
  claudication clients add -name NAME      Make a client and print its key
               [-type custom|claude-code|codex|opencode|crush]
  claudication clients list                List clients
  claudication clients delete -id ID       Delete a client and its key
  claudication backup [FILE]               Snapshot the state to one file
  claudication restore FILE                Put a backup back
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

// openState loads config and opens the database with its sealing key, the
// steps every command shares.
func openState(ctx context.Context, configPath string) (config.Config, *store.Store, error) {
	cfg, err := config.Load(resolveConfigPath(configPath))
	if err != nil {
		return config.Config{}, nil, err
	}
	if err := cfg.EnsureStateDir(); err != nil {
		return config.Config{}, nil, err
	}
	sealer, err := secret.Load(cfg.StateDir)
	if err != nil {
		return config.Config{}, nil, err
	}
	st, err := store.Open(ctx, cfg.DBPath(), sealer)
	if err != nil {
		return config.Config{}, nil, err
	}
	return cfg, st, nil
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

	cfg, st, err := openState(ctx, *configPath)
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

	_, st, err := openState(ctx, *configPath)
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

// shortPeriod renders a rate limit's period the way a person says it.
func shortPeriod(d time.Duration) string {
	switch d {
	case time.Minute:
		return "min"
	case time.Hour:
		return "hour"
	case 24 * time.Hour:
		return "day"
	}
	return d.String()
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
//
// --if-unset is for the installer, which runs on updates too: it claims a
// gateway nobody has claimed and leaves an existing password alone, deciding
// both in the one command. Asking another command "is there an admin?" first
// and setting a password when that failed overwrote the operator's password
// whenever the first command failed for any other reason.
func cmdPasswd(args []string) error {
	fs := flag.NewFlagSet("passwd", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config.yaml (optional)")
	ifUnset := fs.Bool("if-unset", false, "set the password only if none is set yet")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, st, err := openState(ctx, *configPath)
	if err != nil {
		return err
	}
	defer st.Close()

	existed, err := st.AdminExists(ctx)
	if err != nil {
		return err
	}
	if existed && *ifUnset {
		fmt.Println("An admin password is already set; left as it is.")
		return nil
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

func cmdClients(args []string) error {
	if len(args) == 0 {
		return errors.New("clients: expected add, list or delete")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("clients add", flag.ContinueOnError)
		configPath := fs.String("config", "", "path to config.yaml (optional)")
		name := fs.String("name", "", "the client's name (required)")
		typ := fs.String("type", "custom", "custom, claude-code, codex, opencode or crush")
		rpm := fs.Int("rpm", 0, "the client's request allowance (0 = use the global default)")
		ratePeriod := fs.Duration("rate-period", time.Minute,
			"what -rpm is measured over (1m, 1h, 24h)")
		budget := fs.Int64("token-budget", 0,
			"tokens the client may spend per rolling 24 hours (0 = unlimited)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" {
			return errors.New("clients add: -name is required")
		}
		t, ok := clients.Find(*typ)
		if !ok {
			return fmt.Errorf("clients add: no client type %q", *typ)
		}

		_, st, err := openState(ctx, *configPath)
		if err != nil {
			return err
		}
		defer st.Close()

		// A typed client starts with its type's names, as the admin UI makes
		// one. A custom one from here is for a provisioning script, which
		// names real models: direct mode, as every key from before clients
		// became one.
		initial := t.Initial()
		var slots []store.NewSlot
		if !t.Custom {
			for _, a := range initial {
				slots = append(slots, store.NewSlot{Slot: a.Slot, Name: a.Name, Binding: store.Binding{Follows: a.Follows}})
			}
		}
		c, plaintext, err := st.CreateClient(ctx, *name, t.ID, store.KeyLimits{
			RPMLimit:    *rpm,
			RatePeriod:  *ratePeriod,
			TokenBudget: *budget,
		}, slots)
		if err != nil {
			return err
		}
		if t.Custom {
			if err := st.SetClientMode(ctx, c.ID, clients.Direct); err != nil {
				return err
			}
		}
		fmt.Printf("Created %s client %q (id %s)\n\n  %s\n\n", t.Label, c.Name, c.ID, plaintext)
		fmt.Println("Its page in the admin UI shows the key and its setup again until it is first used.")
		return nil

	case "list":
		fs := flag.NewFlagSet("clients list", flag.ContinueOnError)
		configPath := fs.String("config", "", "path to config.yaml (optional)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		_, st, err := openState(ctx, *configPath)
		if err != nil {
			return err
		}
		defer st.Close()

		keys, err := st.ListKeys(ctx)
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			fmt.Println("No clients. Make one with: claudication clients add -name NAME")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tTYPE\tKEY\tCREATED\tLAST USED\tRPM\tTOKENS/DAY")
		for _, k := range keys {
			last := "never"
			if k.LastUsedAt != nil {
				last = k.LastUsedAt.Format(time.RFC3339)
			}
			rpm := "default"
			if k.RPMLimit > 0 {
				rpm = fmt.Sprintf("%d/%s", k.RPMLimit, shortPeriod(k.Period()))
			}
			// Spelled out rather than shown as 0, which reads as "none allowed"
			// where it means the opposite.
			budget := "unlimited"
			if k.TokenBudget > 0 {
				budget = fmt.Sprint(k.TokenBudget)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				k.ID, k.Name, k.ClientType, k.Display(), k.CreatedAt.Format(time.RFC3339), last, rpm, budget)
		}
		return w.Flush()

	case "delete":
		fs := flag.NewFlagSet("clients delete", flag.ContinueOnError)
		configPath := fs.String("config", "", "path to config.yaml (optional)")
		id := fs.String("id", "", "client id to delete (required)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return errors.New("clients delete: -id is required")
		}
		_, st, err := openState(ctx, *configPath)
		if err != nil {
			return err
		}
		defer st.Close()

		if err := st.DeleteKey(ctx, *id); err != nil {
			return err
		}
		fmt.Printf("Deleted client %s and its key\n", *id)
		return nil

	default:
		return fmt.Errorf("clients: unknown subcommand %q", args[0])
	}
}
