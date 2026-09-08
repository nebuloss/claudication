package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"claudication/internal/config"
)

// The two files that are the gateway.
//
// secret.key seals every stored OAuth token, so the database alone restores to
// a set of accounts whose credentials cannot be decrypted — recoverable only by
// going through the browser consent flow again for each one. They travel
// together or the backup is decorative.
const (
	dbEntry  = "claudication.db"
	keyEntry = "secret.key"
)

// cmdBackup writes the state directory to one portable file.
//
// A single file rather than "copy these two things", because the interesting
// failure is the operator who copies one of them, or who copies the database
// with `cp` while the gateway is running and gets a torn WAL. This takes a
// consistent snapshot without stopping the service.
func cmdBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config.yaml (optional)")
	out := fs.String("out", "", "file to write (default claudication-backup-<date>.tar.gz)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cfg, st, _, err := openState(ctx, *configPath)
	if err != nil {
		return err
	}
	defer st.Close()

	target := *out
	if target == "" {
		target = "claudication-backup-" + time.Now().UTC().Format("20060102-150405") + ".tar.gz"
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("%s already exists; move it aside or choose another -out", target)
	}

	// The snapshot goes to a temporary file beside the database, so it lands on
	// the same filesystem and never half-exists at the destination.
	snap := filepath.Join(cfg.StateDir, fmt.Sprintf(".backup-%d.db", time.Now().UnixNano()))
	if err := st.Snapshot(ctx, snap); err != nil {
		return err
	}
	defer os.Remove(snap)

	// 0600 from the moment it exists. This file holds the sealing key and the
	// sealed tokens together, which makes it exactly as sensitive as the state
	// directory — there is no window in which it should be readable by anyone
	// else.
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", target, err)
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	if err := addFile(tw, dbEntry, snap); err != nil {
		return err
	}
	keyPath := filepath.Join(cfg.StateDir, keyEntry)
	if err := addFile(tw, keyEntry, keyPath); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("flush %s: %w", target, err)
	}

	info, _ := os.Stat(target)
	fmt.Printf("Wrote %s (%s)\n", target, humanBytes(info.Size()))
	fmt.Println()
	fmt.Println("This file contains the sealing key and the sealed tokens together,")
	fmt.Println("so it is exactly as sensitive as the state directory: anyone holding")
	fmt.Println("it holds every connected Claude account. Keep it somewhere you would")
	fmt.Println("keep a password.")
	return nil
}

func addFile(tw *tar.Writer, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if err := tw.WriteHeader(&tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	return nil
}

// cmdRestore puts a backup back, refusing to overwrite a gateway that is
// already set up unless told to.
func cmdRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config.yaml (optional)")
	in := fs.String("in", "", "backup file to restore (required)")
	force := fs.Bool("force", false, "replace an existing database and key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return errors.New("restore: -in is required")
	}

	// Config only — deliberately not openState, which would create the very
	// database this is about to replace.
	cfg, err := config.Load(resolveConfigPath(*configPath))
	if err != nil {
		return err
	}
	if err := cfg.EnsureStateDir(); err != nil {
		return err
	}

	dbPath := filepath.Join(cfg.StateDir, dbEntry)
	keyPath := filepath.Join(cfg.StateDir, keyEntry)
	if !*force {
		for _, p := range []string{dbPath, keyPath} {
			if _, err := os.Stat(p); err == nil {
				return fmt.Errorf("%s already exists; stop the gateway and pass -force to replace it", p)
			}
		}
	}

	f, err := os.Open(*in)
	if err != nil {
		return fmt.Errorf("read %s: %w", *in, err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read %s: %w", *in, err)
	}
	defer gz.Close()

	seen := map[string]bool{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", *in, err)
		}
		// Only the two names this writes, matched exactly. An archive is
		// attacker-shaped input: a path with .. or a leading / in it would
		// otherwise write wherever it liked.
		name := filepath.Base(filepath.Clean("/" + h.Name))
		var dest string
		switch name {
		case dbEntry:
			dest = dbPath
		case keyEntry:
			dest = keyPath
		default:
			continue
		}
		if err := writeFile(dest, tr); err != nil {
			return err
		}
		seen[name] = true
	}

	if !seen[dbEntry] {
		return fmt.Errorf("%s contains no %s; is it a claudication backup?", *in, dbEntry)
	}
	if !seen[keyEntry] {
		// Recoverable only by re-authorising every account, so say so rather
		// than letting it be discovered later.
		fmt.Println("Warning: no secret.key in the backup. The accounts are restored but")
		fmt.Println("their stored tokens cannot be decrypted; each will need connecting again.")
	}

	// The WAL and shared-memory files belong to the database that was just
	// replaced. Leaving them would let SQLite apply a stale log over a restored
	// file, which is a worse outcome than any of the ones this command exists
	// to prevent.
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(dbPath + suffix)
	}

	fmt.Printf("Restored into %s\n", cfg.StateDir)
	fmt.Println("Start the gateway and sign in as before.")
	return nil
}

func writeFile(path string, r io.Reader) error {
	tmp := path + ".restoring"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("flush %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	// Renamed into place, so a restore that dies halfway leaves the previous
	// file rather than a truncated one.
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
