package clientconfig

import (
	"context"
	"errors"
	"fmt"
	"time"

	"claudication/internal/service/install"
	"claudication/internal/store"
)

// Secrets reads a key's secret, to make sure an update does not carry it.
type Secrets interface {
	KeySecret(ctx context.Context, id string) (string, error)
}

// MakeUpdate writes a configured client's update files for base and keeps
// them under a new update link, answering its token and when it stops
// working. One path for the admin page's Update config and the MCP server's
// update_config, so what an agent is handed is what the operator would be.
//
// The files are refused if the key is in them after all — the secret, or its
// prefix, which is enough of it to know it by: an update command goes on a
// screen and into an agent's transcript, and the writer leaving the key out
// is the rule this holds it to.
func MakeUpdate(ctx context.Context, secrets Secrets, links *install.Links, c store.Client,
	s Setup, base string) (string, time.Time, error) {
	files := Update(c.Type, c.Spec().Label, s, base)
	if files == nil {
		return "", time.Time{}, errors.New("a custom client has no files to update")
	}
	marks := []string{store.KeyPrefix + c.Prefix}
	switch plaintext, err := secrets.KeySecret(ctx, c.ID); {
	case err == nil:
		marks = append(marks, plaintext)
	case !errors.Is(err, store.ErrSecretNotKept):
		return "", time.Time{}, fmt.Errorf("read the key's secret: %w", err)
	}
	if p := install.Carrying(files, marks...); p != "" {
		return "", time.Time{}, fmt.Errorf("%s carries the client's key; an update carries none, and keeps the one on disk", p)
	}
	return links.Create(install.Bundle{ClientID: c.ID, Key: c.Name, Files: files, Update: true})
}
