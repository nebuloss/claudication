// Package install serves one-time setup scripts: a client's configuration,
// written to the right places by one pasted command.
//
// The admin UI already has every client's files, with a key and the gateway's
// address inside and the models tailored to what that key is offered — it is
// what the Clients page shows. Rather than build them a second time here, the
// page hands over every client's files, and this keeps them under a random
// token for a few minutes. One address then serves them, the query saying
// which shell and which clients:
//
//	curl -fsSL 'https://gateway/install?token=<t>&shell=sh&client=crush' | sh
//	irm 'https://gateway/install?token=<t>&shell=ps1' | iex
//
// A token works for as many runs as its few minutes allow — a client per
// command, or the same command on two machines — and then is gone, because
// the script carries the key and a command copied and never run should not be
// a key waiting in memory. Held in memory rather than the database for the
// same reason: a restart forgets them, which costs nothing but a new command.
//
// The scripts back up every file they change, beside it, so running a newer
// command over an older setup is an update and never a loss. A client's own
// config — crush's, opencode's, Codex's — is merged rather than replaced: the
// gateway's sections are rewritten and the rest of the file is left alone
// (see Merge, and the helpers under merge/).
package install

import (
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
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
)

// File is one file a client is configured with, and where it goes.
type File struct {
	// ClientID is the client it belongs to, as the install address names it:
	// claude-code, codex, opencode, crush.
	ClientID string `json:"client_id"`
	// Client is the client's name, for the script's messages.
	Client string `json:"client"`
	// Path is where it goes, relative to the home directory, with forward
	// slashes: ".codex/config.toml".
	Path string `json:"path"`
	// WindowsPath is where it goes on Windows when that differs, relative to
	// the home directory as well: crush keeps its config under AppData.
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
}

// Merge is how a file is folded into a client config that holds more than
// the gateway's settings: crush's other providers, opencode's theme, Codex's
// MCP servers. Writing the whole file over it lost all of that.
type Merge struct {
	// Format is "json" (JSONC too) or "toml".
	Format string `json:"format"`
	// Owns is, for JSON, the dotted paths the gateway owns: each is replaced
	// whole, so a setting the gateway stopped sending does not linger inside
	// it. The file's other top-level keys are added only when absent. TOML
	// needs none: what the gateway owns there is what its file defines — the
	// top-level keys and the tables — since none of it is a map shared with
	// anything else.
	Owns []string `json:"owns,omitempty"`
}

// ownedPath is a dotted JSON path of plain names, safe to pass to a shell.
var ownedPath = regexp.MustCompile(`^[A-Za-z0-9_$-]+(\.[A-Za-z0-9_$-]+)*$`)

func (m *Merge) validate() error {
	if m == nil {
		return nil
	}
	switch m.Format {
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

// Bundle is what one command installs.
type Bundle struct {
	// Key is the API key's name, for the script's first line.
	Key   string
	Files []File
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
		for _, p := range paths {
			if !safePath.MatchString(p) || strings.Contains("/"+p+"/", "/../") || strings.Contains("/"+p+"/", "/./") {
				return fmt.Errorf("%q is not a path under the home directory", p)
			}
		}
		if err := f.Merge.validate(); err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
		total += len(f.Body)
	}
	if total > maxBytes {
		return fmt.Errorf("the files come to more than %d bytes", maxBytes)
	}
	return nil
}

// Links holds the bundles waiting to be fetched, each under its token.
type Links struct {
	ttl time.Duration
	now func() time.Time

	mu sync.Mutex
	m  map[string]link
}

type link struct {
	bundle  Bundle
	expires time.Time
}

// New returns an empty set of links that each live for ttl.
func New(ttl time.Duration) *Links {
	return &Links{ttl: ttl, now: time.Now, m: map[string]link{}}
}

// Create keeps a bundle and returns the token that fetches it, and when that
// token stops working.
func (l *Links) Create(b Bundle) (string, time.Time, error) {
	if err := b.Validate(); err != nil {
		return "", time.Time{}, err
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
	l.m[token] = link{bundle: b, expires: expires}
	return token, expires, nil
}

// Get returns the bundle under a token while it lasts. An unknown or expired
// token is false.
func (l *Links) Get(token string) (Bundle, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep()
	got, ok := l.m[token]
	return got.bundle, ok
}

// For is the part of a bundle for the clients named, comma-separated; every
// client when none is named. A name the bundle does not have selects nothing,
// so a mistyped client fails rather than installing everything.
func (b Bundle) For(clients string) Bundle {
	if strings.TrimSpace(clients) == "" {
		return b
	}
	want := map[string]bool{}
	for _, c := range strings.Split(clients, ",") {
		want[strings.TrimSpace(c)] = true
	}
	out := Bundle{Key: b.Key}
	for _, f := range b.Files {
		if want[f.ClientID] {
			out.Files = append(out.Files, f)
		}
	}
	return out
}

// sweep drops expired links. Called with the lock held, on every use, so
// nothing needs a timer of its own.
func (l *Links) sweep() {
	now := l.now()
	for t, v := range l.m {
		if !now.Before(v.expires) {
			delete(l.m, t)
		}
	}
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
	var sourced []string
	for _, f := range b.Files {
		path := `$HOME/` + f.Path
		if f.Merge == nil {
			fmt.Fprintf(&s, "\n# %s\nput \"%s\" ", f.Client, path)
		} else {
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

func (b Bundle) merges() bool {
	for _, f := range b.Files {
		if f.Merge != nil {
			return true
		}
	}
	return false
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
		{"merge.py", mergePy}, {"merge.js", mergeJS}, {"merge.awk", mergeAwk},
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
	if b.merges() {
		s.WriteString(mergePS1)
		s.WriteString(`
# A file that is not there yet is written; one that cannot be read as what it
# should be is left as it was, with the gateway's version beside it.
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
	for _, f := range b.Files {
		if f.Env {
			fmt.Fprintf(&s, "\n# %s: user environment variables\n", f.Client)
			for _, line := range strings.Split(f.Body, "\n") {
				m := exportLine.FindStringSubmatch(strings.TrimSpace(line))
				if m == nil {
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
	}
	s.WriteString("\nWrite-Host 'claudication: done. Open a new terminal for the variables to apply.'\n")
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
