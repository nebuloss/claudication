// Package install serves one-time setup scripts: a client's configuration,
// written to the right places by one pasted command.
//
// The admin UI already has the client's files, with its key and the
// gateway's address inside and each slot's model facts tailored to what it
// serves — it is what the client's page shows. Rather than build them a
// second time here, the page hands them over, and this keeps them under a
// random token for a few minutes. One address then serves them, the query
// saying which shell:
//
//	curl -fsSL 'https://gateway/install?token=<t>&shell=sh' | sh
//	irm 'https://gateway/install?token=<t>&shell=ps1' | iex
//
// A token serves one fetch and then is spent: the script carries the key, and
// the fetch is what tells the client's page that the client is set up, and
// from where. One left unused is gone after its few minutes, because a
// command copied and never run should not be a key waiting in memory. Held in
// memory rather than the database for the same reason: a restart forgets
// them, which costs nothing but a new command.
//
// The scripts back up every file they change, beside it, so running a newer
// command over an older setup is an update and never a loss. A client's own
// config — crush's, opencode's, Codex's — is merged rather than replaced: the
// gateway's sections are rewritten and the rest of the file is left alone
// (see Merge, and the helpers under merge/).
//
// An update link (Bundle.Update) is the same thing without the key: once a
// client is set up its key is never shown again, yet its model names and
// what they serve still change. Its files carry no key — the admin API
// refuses one that does — and own only what is not the key: a JSON merge
// names the model paths alone, and the Claude Code env file is rewritten
// keeping its key's line from the file on disk (the "env" merge). A file
// whose key the update keeps from disk must be there (File.Existing): the
// script checks every one before it writes anything, and refuses a machine
// that was never set up rather than leave it a configuration with no key.
package install

import (
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// The merge helpers, carried inside the scripts. JSON on Linux and macOS goes
// through python3 or node, whichever the machine has, since sh has nothing
// that reads it; TOML through awk, which every one has. Windows has
// PowerShell for both.
var (
	//go:embed merge/merge.py
	mergePy string
	//go:embed merge/merge.js
	mergeJS string
	//go:embed merge/merge.awk
	mergeAwk string
	//go:embed merge/merge.ps1
	mergePS1 string
	//go:embed merge/env.awk
	envAwk string
)

// File is one file a client is configured with, and where it goes.
type File struct {
	// ClientID is the type of client it configures: claude-code, codex,
	// opencode, crush.
	ClientID string `json:"client_id"`
	// Client is the client's name, for the script's messages.
	Client string `json:"client"`
	// Path is where it goes, relative to the home directory, with forward
	// slashes: ".codex/config.toml".
	Path string `json:"path"`
	// WindowsPath is where it goes on Windows when that differs, relative to
	// the home directory as well. No client needs one today: crush, which
	// had one, reads ~/.config on Windows too.
	WindowsPath string `json:"windows_path,omitempty"`
	// Env marks a file of `export NAME=value` lines meant to be in every
	// shell: sourced from the shell profile on Linux and macOS, and set as the
	// user's environment variables on Windows, where there is no profile to
	// source it from.
	Env  bool   `json:"env,omitempty"`
	Body string `json:"body"`
	// Merge, when set, folds the file into the one the client already has
	// instead of replacing it. Absent for a file that is the gateway's alone.
	Merge *Merge `json:"merge,omitempty"`
	// Stale is where an earlier install wrote this file on Windows by mistake,
	// and what it owned there: the script takes those keys back out of that
	// file, if they are in it, and leaves the rest. crush's config went to its
	// data file under AppData, which holds the user's model picks.
	Stale *Stale `json:"stale,omitempty"`
	// Existing is a file that must already be there: an update's file whose
	// key stays as it is on disk. Written fresh, it would configure the
	// client without one, so a script finding it missing changes nothing
	// and says the machine was never set up with this client.
	Existing bool `json:"existing,omitempty"`
}

// NeverSetUp is what a script says when an update finds no setup to update.
const NeverSetUp = "this machine was never set up with this client; regenerate the key to install it"

// Stale is a wrong place an earlier install wrote to, on Windows.
type Stale struct {
	WindowsPath string   `json:"windows_path"`
	Owns        []string `json:"owns"`
}

// Merge is how a file is folded into a client config that holds more than
// the gateway's settings: crush's other providers, opencode's theme, Codex's
// MCP servers. Writing the whole file over it lost all of that.
type Merge struct {
	// Format is "json" (JSONC too), "toml", or "env" for an env file.
	Format string `json:"format"`
	// Owns is, for JSON, the dotted paths the gateway owns: each is replaced
	// whole, so a setting the gateway stopped sending does not linger inside
	// it. The file's other top-level keys are added only when absent. TOML
	// needs none: what the gateway owns there is what its file defines — the
	// top-level keys and the tables — since none of it is a map shared with
	// anything else.
	Owns []string `json:"owns,omitempty"`
	// Keep is, for an env file, the variables whose lines stay as they are in
	// the file on disk — on Windows, the user variables as they are set — the
	// rest being rewritten from the new file: an update keeping the key the
	// client was installed with. Each must be there, or nothing is written.
	Keep []string `json:"keep,omitempty"`
}

// ownedPath is a dotted JSON path of plain names, safe to pass to a shell.
var ownedPath = regexp.MustCompile(`^[A-Za-z0-9_$-]+(\.[A-Za-z0-9_$-]+)*$`)

// envName is a variable's name, safe to pass to a shell.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (m *Merge) validate(env bool) error {
	if m == nil {
		return nil
	}
	if (m.Format == "env") != env {
		return errors.New("an env file merges as env, and only an env file does")
	}
	if m.Format != "env" && len(m.Keep) != 0 {
		return errors.New("only an env merge keeps variables")
	}
	switch m.Format {
	case "env":
		if len(m.Keep) == 0 || len(m.Owns) != 0 {
			return errors.New("an env merge names the variables it keeps, and owns nothing")
		}
		for _, v := range m.Keep {
			if !envName.MatchString(v) {
				return fmt.Errorf("%q is not a variable's name", v)
			}
		}
	case "json":
		if len(m.Owns) == 0 {
			return errors.New("a JSON merge names what it owns")
		}
		for _, p := range m.Owns {
			if !ownedPath.MatchString(p) {
				return fmt.Errorf("%q is not a path of plain names", p)
			}
		}
	case "toml":
		if len(m.Owns) != 0 {
			return errors.New("a TOML merge owns what its file defines, and names nothing")
		}
	default:
		return fmt.Errorf("no merge for %q files", m.Format)
	}
	return nil
}

// Bundle is what one command installs: one client's files.
type Bundle struct {
	// ClientID is the client's id, which the link is kept under: a client
	// has one live link of each kind at a time.
	ClientID string
	// Key is the client's name, for the script's first line.
	Key   string
	Files []File
	// Update is a link that updates a client already set up, and carries no
	// key: its fetch does not mark the client configured (it is already),
	// and its state is the client's update state rather than its install's,
	// so the record of where the client was installed from stays.
	Update bool
}

// Limits on what the admin UI may hand over: enough for every client there is
// with room to spare, and a ceiling on what a link can hold in memory.
const (
	maxFiles = 16
	maxBytes = 4 << 20
)

// safePath is a home-relative path made of ordinary names: no absolute paths,
// no "..", nothing a shell or PowerShell would read as anything but text.
var safePath = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)

// Validate refuses a bundle that could write anywhere but under the home
// directory, or that is larger than any real configuration.
func (b Bundle) Validate() error {
	if len(b.Files) == 0 {
		return errors.New("nothing to install")
	}
	if len(b.Files) > maxFiles {
		return fmt.Errorf("at most %d files", maxFiles)
	}
	total := 0
	for _, f := range b.Files {
		paths := []string{f.Path}
		if f.WindowsPath != "" {
			paths = append(paths, f.WindowsPath)
		}
		if f.Stale != nil {
			if f.Stale.WindowsPath == "" || len(f.Stale.Owns) == 0 {
				return fmt.Errorf("%s: a stale place names a path and what to take out of it", f.Path)
			}
			for _, o := range f.Stale.Owns {
				if !ownedPath.MatchString(o) {
					return fmt.Errorf("%s: %q is not a path of plain names", f.Path, o)
				}
			}
			paths = append(paths, f.Stale.WindowsPath)
		}
		for _, p := range paths {
			if !safePath.MatchString(p) || strings.Contains("/"+p+"/", "/../") || strings.Contains("/"+p+"/", "/./") {
				return fmt.Errorf("%q is not a path under the home directory", p)
			}
		}
		if err := f.Merge.validate(f.Env); err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
		// An update carries no key, so an env file written whole would put
		// the placeholder where the key was.
		if b.Update && f.Env && f.Merge == nil {
			return fmt.Errorf("%s: an update keeps the key's line of an env file, and names it", f.Path)
		}
		total += len(f.Body)
	}
	if total > maxBytes {
		return fmt.Errorf("the files come to more than %d bytes", maxBytes)
	}
	return nil
}

// Links holds each client's install link: the bundle waiting to be fetched
// under a token, or, once fetched, the record of it.
type Links struct {
	ttl time.Duration
	now func() time.Time

	mu sync.Mutex
	// byClient is each client's latest link of each kind, install and
	// update; a new one replaces the last of its kind, and the old token
	// stops working.
	byClient map[linkKey]*link
}

type linkKey struct {
	client string
	update bool
}

type link struct {
	token   string
	bundle  Bundle
	expires time.Time
	used    *Use
}

// Use is the one fetch a link served.
type Use struct {
	At    time.Time `json:"at"`
	IP    string    `json:"ip"`
	Shell string    `json:"shell"`
}

// The states a link is in, as the client's page shows them.
const (
	Waiting = "waiting"
	Used    = "used"
	Expired = "expired"
	// None is a client with no link made since the gateway started.
	None = "none"
)

// State is a client's link as its page shows it, polled while it is open.
type State struct {
	State   string    `json:"state"`
	Token   string    `json:"token,omitempty"`
	Expires time.Time `json:"expires_at"`
	Used    *Use      `json:"used,omitempty"`
}

// New returns an empty set of links that each live for ttl.
func New(ttl time.Duration) *Links {
	return &Links{ttl: ttl, now: time.Now, byClient: map[linkKey]*link{}}
}

// Create keeps a client's bundle and returns the token that fetches it once,
// and when that token stops working. The client's previous link of the same
// kind, if any, is gone; an update link leaves the install link's record be.
func (l *Links) Create(b Bundle) (string, time.Time, error) {
	if err := b.Validate(); err != nil {
		return "", time.Time{}, err
	}
	if b.ClientID == "" {
		return "", time.Time{}, errors.New("a link is a client's")
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("make install token: %w", err)
	}
	token := hex.EncodeToString(raw)
	expires := l.now().Add(l.ttl)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep()
	l.byClient[linkKey{b.ClientID, b.Update}] = &link{token: token, bundle: b, expires: expires}
	return token, expires, nil
}

// sweep empties every link past its time that was never fetched: its files
// carry the client's key, and an expired link has no use for them. What is
// left is what StateOf needs to say "expired". Run on every call, so a key
// waits in memory no longer than the link's own lifetime plus the next call,
// rather than until the process restarts. A fetched link already holds no
// files (Take).
func (l *Links) sweep() {
	now := l.now()
	for _, k := range l.byClient {
		if k.used == nil && !now.Before(k.expires) {
			k.token = ""
			k.bundle = Bundle{ClientID: k.bundle.ClientID, Update: k.bundle.Update}
		}
	}
}

// ErrUsed is Take's answer for a link already fetched once.
var ErrUsed = errors.New("this command was already used; make a new one on the client's page")

// ErrUnknown is Take's answer for a token that is no client's current link,
// or one past its time.
var ErrUnknown = errors.New("this command has expired or its token is wrong; make a new one on the client's page")

// Take is the one fetch a link allows: the bundle, the first time and while
// it lasts; ErrUsed after that, and ErrUnknown for a token that is no live
// link. Single use because the script carries the client's key, and because
// "used" is what tells the operator the client is now set up — a link that
// served twice would not say which machine took it.
func (l *Links) Take(token, ip, shell string) (Bundle, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep()
	for _, k := range l.byClient {
		if token == "" || k.token != token {
			continue
		}
		switch {
		case k.used != nil:
			return Bundle{}, ErrUsed
		case !l.now().Before(k.expires):
			return Bundle{}, ErrUnknown
		}
		k.used = &Use{At: l.now(), IP: ip, Shell: shell}
		b := k.bundle
		// The files carry the key; once served there is no reason to hold
		// them.
		k.bundle = Bundle{ClientID: b.ClientID, Key: b.Key, Update: b.Update}
		return b, nil
	}
	return Bundle{}, ErrUnknown
}

// StateOf is a client's latest install link as its page shows it.
func (l *Links) StateOf(clientID string) State { return l.stateOf(linkKey{clientID, false}) }

// UpdateStateOf is a client's latest update link as its page shows it.
func (l *Links) UpdateStateOf(clientID string) State { return l.stateOf(linkKey{clientID, true}) }

func (l *Links) stateOf(key linkKey) State {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep()
	k, ok := l.byClient[key]
	switch {
	case !ok:
		return State{State: None}
	case k.used != nil:
		return State{State: Used, Expires: k.expires, Used: k.used}
	case !l.now().Before(k.expires):
		return State{State: Expired, Expires: k.expires}
	}
	return State{State: Waiting, Token: k.token, Expires: k.expires}
}

// Forget drops both of a client's links: its key changed, or the client is
// gone. An update link goes too, since what it keeps on disk is the old key.
func (l *Links) Forget(clientID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byClient, linkKey{clientID, false})
	delete(l.byClient, linkKey{clientID, true})
}

// heredocEnd is a terminator no file can contain by accident: random, and
// checked against the body anyway.
func heredocEnd(body string) string {
	for {
		raw := make([]byte, 6)
		_, _ = rand.Read(raw)
		end := "CLAUDICATION_" + strings.ToUpper(hex.EncodeToString(raw))
		if !strings.Contains(body, end) {
			return end
		}
	}
}

// profileLine sources the Claude Code file from a shell profile, guarded so a
// profile still loads if the file is removed, and marked so it is added once.
const profileMarker = "# claudication"

// Shell is the script for Linux and macOS: POSIX sh, run as `curl … | sh`.
func Shell(b Bundle) string {
	var s strings.Builder
	fmt.Fprintf(&s, "#!/bin/sh\n# claudication: client configuration for the key %q.\n", b.Key)
	if b.Update {
		s.WriteString("# An update: the model names and what they serve. The key stays as it is here.\n")
	}
	s.WriteString(`set -eu
stamp=$(date +%Y%m%d%H%M%S)
put() {
  # The file goes in place; whatever was there is kept beside it.
  mkdir -p "$(dirname "$1")"
  if [ -f "$1" ]; then cp "$1" "$1.bak.$stamp"; fi
  cat > "$1"
  echo "claudication: wrote $1"
}
`)
	if b.merges() {
		s.WriteString(shellMergeHelpers())
	}
	s.WriteString(shellPreflight(b))
	var sourced []string
	for _, f := range b.Files {
		path := `$HOME/` + f.Path
		switch {
		case f.Merge == nil:
			fmt.Fprintf(&s, "\n# %s\nput \"%s\" ", f.Client, path)
		case f.Merge.Format == "env":
			fmt.Fprintf(&s, "\n# %s\nmerge_env \"%s\" %s ", f.Client, path, strings.Join(f.Merge.Keep, " "))
		default:
			fmt.Fprintf(&s, "\n# %s\nmerge_into \"%s\" %s", f.Client, path, f.Merge.Format)
			for _, p := range f.Merge.Owns {
				s.WriteString(" " + shQuote(p))
			}
			s.WriteString(" ")
		}
		writeHeredoc(&s, f.Body)
		if f.Env {
			sourced = append(sourced, path)
		}
	}
	for _, p := range sourced {
		// Every shell that already has a profile sources it; with none, the
		// one sh itself reads is made.
		fmt.Fprintf(&s, `
added=""
for rc in "$HOME/.bashrc" "$HOME/.zshrc"; do
  if [ -f "$rc" ]; then
    if ! grep -qF '%[2]s' "$rc"; then
      printf '\n%[1]s\n[ -f "%[2]s" ] && . "%[2]s"\n' >> "$rc"
      echo "claudication: $rc now sources %[2]s"
    fi
    added=1
  fi
done
if [ -z "$added" ]; then
  if ! grep -qsF '%[2]s' "$HOME/.profile"; then
    printf '\n%[1]s\n[ -f "%[2]s" ] && . "%[2]s"\n' >> "$HOME/.profile"
    echo "claudication: $HOME/.profile now sources %[2]s"
  fi
fi
`, profileMarker, p)
	}
	if len(sourced) > 0 {
		s.WriteString("\necho \"claudication: done. Open a new shell, or run: . " + sourced[0] + "\"\n")
	} else {
		s.WriteString("\necho \"claudication: done.\"\n")
	}
	return s.String()
}

// writeHeredoc feeds body to the command just written, as a quoted heredoc so
// nothing in it is expanded.
func writeHeredoc(s *strings.Builder, body string) {
	end := heredocEnd(body)
	fmt.Fprintf(s, "<<'%s'\n%s", end, body)
	if !strings.HasSuffix(body, "\n") {
		s.WriteString("\n")
	}
	s.WriteString(end + "\n")
}

// merges reports whether any file is merged, in one of formats or any.
func (b Bundle) merges(formats ...string) bool {
	for _, f := range b.Files {
		if f.Merge != nil && (len(formats) == 0 || slices.Contains(formats, f.Merge.Format)) {
			return true
		}
	}
	return false
}

// preflight is, for each file an update needs to find on disk, the file and
// the variables in it that must be there. An install has none.
func (b Bundle) preflight() []File {
	var out []File
	for _, f := range b.Files {
		if f.Existing || (f.Merge != nil && f.Merge.Format == "env") {
			out = append(out, f)
		}
	}
	return out
}

// shellPreflight checks, before the script writes anything, that each file
// an update keeps the key of is there and holds it: one update half applied
// to a machine never set up would leave it configured without a key.
func shellPreflight(b Bundle) string {
	need := b.preflight()
	if len(need) == 0 {
		return ""
	}
	var s strings.Builder
	fmt.Fprintf(&s, "\n# What this update keeps is on disk, or nothing is written.\nnever() { echo \"claudication: $1: %s.\" >&2; exit 1; }\n", NeverSetUp)
	for _, f := range need {
		path := `$HOME/` + f.Path
		fmt.Fprintf(&s, "[ -s \"%[1]s\" ] || never \"there is no %[1]s\"\n", path)
		if f.Merge != nil {
			for _, v := range f.Merge.Keep {
				fmt.Fprintf(&s, "grep -q '^export %[2]s=' \"%[1]s\" || never \"%[1]s sets no %[2]s\"\n", path, v)
			}
		}
	}
	return s.String()
}

// shellMergeHelpers defines merge_into, which folds the file on its stdin
// into the one at $1.
//
// A file that is not there yet, or empty, is simply written. One that cannot
// be merged — JSON on a machine with neither python3 nor node, or a file that
// does not parse — is left exactly as it is, with the gateway's version
// beside it as .claudication and a line saying so: replacing it would be the
// loss this exists to prevent.
func shellMergeHelpers() string {
	var s strings.Builder
	s.WriteString(`
helpers=$(mktemp -d)
trap 'rm -rf "$helpers"' EXIT
`)
	for _, h := range []struct{ name, body string }{
		{"merge.py", mergePy}, {"merge.js", mergeJS}, {"merge.awk", mergeAwk}, {"env.awk", envAwk},
	} {
		fmt.Fprintf(&s, "cat > \"$helpers/%s\" ", h.name)
		writeHeredoc(&s, h.body)
	}
	s.WriteString(`# macOS ships a python3 that only offers to install the real one, so a
# python3 that cannot run is no python3.
have_python() { command -v python3 >/dev/null 2>&1 && python3 -c '' >/dev/null 2>&1; }
merge_into() {
  target=$1; format=$2; shift 2
  mkdir -p "$(dirname "$target")"
  cat > "$helpers/new"
  if [ ! -s "$target" ]; then
    mv "$helpers/new" "$target"
    echo "claudication: wrote $target"
    return 0
  fi
  cp "$target" "$target.bak.$stamp"
  status=0
  case "$format" in
    toml)
      awk -v new="$helpers/new" -f "$helpers/merge.awk" "$target" > "$helpers/merged" || status=$?
      [ "$status" -ne 0 ] || cat "$helpers/merged" > "$target" ;;
    json)
      if have_python; then python3 -I "$helpers/merge.py" "$target" "$helpers/new" "$@" || status=$?
      elif command -v node >/dev/null 2>&1; then node "$helpers/merge.js" "$target" "$helpers/new" "$@" || status=$?
      else status=127; fi ;;
  esac
  case "$status" in
    0) echo "claudication: updated $target, keeping the rest of it (it was $target.bak.$stamp)" ;;
    127)
      mv "$helpers/new" "$target.claudication"
      echo "claudication: left $target as it was: merging into it needs python3 or node. The gateway's settings are in $target.claudication" >&2 ;;
    *)
      mv "$helpers/new" "$target.claudication"
      echo "claudication: left $target as it was: it could not be read. The gateway's settings are in $target.claudication" >&2 ;;
  esac
  rm -f "$helpers/new" "$helpers/merged"
}
# An env file rewritten from the one on stdin, keeping the lines of the
# variables named from the file at $1: the key it was installed with, which an
# update does not carry. The preflight has checked the file and the lines are
# there.
merge_env() {
  target=$1; shift
  cat > "$helpers/new"
  cp "$target" "$target.bak.$stamp"
  awk -v keep="$*" -f "$helpers/env.awk" "$target" "$helpers/new" > "$helpers/merged"
  cat "$helpers/merged" > "$target"
  rm -f "$helpers/new" "$helpers/merged"
  echo "claudication: updated $target, keeping its $* (it was $target.bak.$stamp)"
}
`)
	return s.String()
}

// exportLine is `export NAME=value`, the only form the env files use.
var exportLine = regexp.MustCompile(`^export ([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

// PowerShell is the script for Windows, run as `irm … | iex`.
//
// Files are carried base64-encoded, so nothing in them — quotes, backticks,
// a $ — is ever read as PowerShell. An env file's variables are set for the
// user instead, which is what every new terminal then has.
func PowerShell(b Bundle) string {
	var s strings.Builder
	fmt.Fprintf(&s, "# claudication: client configuration for the key %s.\n", psQuote(b.Key))
	if b.Update {
		s.WriteString("# An update: the model names and what they serve. The key stays as it is here.\n")
	}
	s.WriteString(`$ErrorActionPreference = 'Stop'
$stamp = Get-Date -Format 'yyyyMMddHHmmss'
function Put-ClaudicationFile([string]$rel, [string]$b64) {
  $path = Join-Path $HOME $rel
  New-Item -ItemType Directory -Force -Path (Split-Path -Parent $path) | Out-Null
  if (Test-Path $path) { Copy-Item $path "$path.bak.$stamp" }
  [IO.File]::WriteAllBytes($path, [Convert]::FromBase64String($b64))
  Write-Host "claudication: wrote $path"
}
`)
	if b.merges("json", "toml") {
		s.WriteString(mergePS1)
		s.WriteString(`
# A file that is not there yet is written; one that cannot be read as what it
# should be is left as it was, with the gateway's version beside it.
# Takes what an earlier install wrongly wrote to $rel back out of it, keeping
# a backup; nothing happens when the file is not there or holds none of it.
function Clear-ClaudicationStale([string]$rel, [string[]]$owned) {
  $path = Join-Path $HOME $rel
  if (-not (Test-Path $path)) { return }
  $copy = "$path.bak.$stamp"
  Copy-Item $path $copy
  $removed = $false
  try { $removed = Remove-ClaudicationJson $path $owned } catch { $removed = $false }
  if ($removed) {
    Write-Host "claudication: took what an earlier install put in $path back out (it was $copy)"
  } else {
    Remove-Item $copy
  }
}

function Merge-ClaudicationFile([string]$rel, [string]$format, [string]$b64, [string[]]$owned) {
  $path = Join-Path $HOME $rel
  $new = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($b64))
  if (-not (Test-Path $path) -or (Get-Item $path).Length -eq 0) { Put-ClaudicationFile $rel $b64; return }
  Copy-Item $path "$path.bak.$stamp"
  $ok = $true
  try {
    if ($format -eq 'toml') { Merge-ClaudicationToml $path $new } else { $ok = Merge-ClaudicationJson $path $new $owned }
  } catch { $ok = $false }
  if ($ok) {
    Write-Host "claudication: updated $path, keeping the rest of it (it was $path.bak.$stamp)"
  } else {
    [IO.File]::WriteAllText("$path.claudication", $new, (New-Object Text.UTF8Encoding $false))
    Write-Warning "claudication: left $path as it was: it could not be read. The gateway's settings are in $path.claudication"
  }
}
`)
	}
	s.WriteString(psPreflight(b))
	for _, f := range b.Files {
		if f.Env {
			var keep []string
			if f.Merge != nil {
				keep = f.Merge.Keep
			}
			fmt.Fprintf(&s, "\n# %s: user environment variables\n", f.Client)
			for _, line := range strings.Split(f.Body, "\n") {
				m := exportLine.FindStringSubmatch(strings.TrimSpace(line))
				// A kept variable stays as the user has it set: the key, which
				// an update does not carry.
				if m == nil || slices.Contains(keep, m[1]) {
					continue
				}
				fmt.Fprintf(&s, "[Environment]::SetEnvironmentVariable(%s, %s, 'User')\n",
					psQuote(m[1]), psQuote(unquote(m[2])))
			}
			fmt.Fprintf(&s, "Write-Host %s\n", psQuote("claudication: "+f.Client+" variables set for new terminals"))
			continue
		}
		path := f.Path
		if f.WindowsPath != "" {
			path = f.WindowsPath
		}
		rel := psQuote(strings.ReplaceAll(path, "/", `\`))
		b64 := base64.StdEncoding.EncodeToString([]byte(f.Body))
		if f.Merge == nil {
			fmt.Fprintf(&s, "\n# %s\nPut-ClaudicationFile %s '%s'\n", f.Client, rel, b64)
			continue
		}
		owns := make([]string, len(f.Merge.Owns))
		for i, p := range f.Merge.Owns {
			owns[i] = psQuote(p)
		}
		fmt.Fprintf(&s, "\n# %s\nMerge-ClaudicationFile %s %s '%s' @(%s)\n", f.Client, rel,
			psQuote(f.Merge.Format), b64, strings.Join(owns, ", "))
		if f.Stale != nil {
			stale := make([]string, len(f.Stale.Owns))
			for i, p := range f.Stale.Owns {
				stale[i] = psQuote(p)
			}
			fmt.Fprintf(&s, "Clear-ClaudicationStale %s @(%s)\n",
				psQuote(strings.ReplaceAll(f.Stale.WindowsPath, "/", `\`)), strings.Join(stale, ", "))
		}
	}
	s.WriteString("\nWrite-Host 'claudication: done. Open a new terminal for the variables to apply.'\n")
	return s.String()
}

// psPreflight is shellPreflight for Windows, where an env file is the user's
// variables: each kept one must be set, and each file that must exist is
// there, before anything is written. A throw under ErrorActionPreference Stop
// ends the script that iex is running.
func psPreflight(b Bundle) string {
	need := b.preflight()
	if len(need) == 0 {
		return ""
	}
	var s strings.Builder
	s.WriteString("\n# What this update keeps is there, or nothing is written.\n")
	for _, f := range need {
		if f.Env {
			if f.Merge == nil {
				continue
			}
			for _, v := range f.Merge.Keep {
				fmt.Fprintf(&s, "if (-not [Environment]::GetEnvironmentVariable(%s, 'User')) { throw %s }\n",
					psQuote(v), psQuote("claudication: the user variable "+v+" is not set: "+NeverSetUp+"."))
			}
			continue
		}
		path := f.Path
		if f.WindowsPath != "" {
			path = f.WindowsPath
		}
		rel := strings.ReplaceAll(path, "/", `\`)
		fmt.Fprintf(&s, "if (-not (Test-Path (Join-Path $HOME %s))) { throw %s }\n",
			psQuote(rel), psQuote("claudication: there is no ~\\"+rel+": "+NeverSetUp+"."))
	}
	return s.String()
}

// psQuote is a single-quoted PowerShell string: only ' needs doubling.
func psQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

// unquote drops one level of shell quoting from an export's value.
func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// Failure is the script sent instead when there is nothing to install — a
// token unknown or expired, or no client by the names asked for: it fails
// loudly rather than as an HTML page piped into a shell.
func Failure(powershell bool, msg string) string {
	msg = "claudication: " + msg
	if powershell {
		return "Write-Error " + psQuote(msg) + "\n"
	}
	return "echo " + shQuote(msg) + " >&2\nexit 1\n"
}

// shQuote is a single-quoted sh string.
func shQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}
