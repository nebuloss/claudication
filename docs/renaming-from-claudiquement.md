# Renaming from claudiquement

The project was called `claudiquement` before its first release. Nothing was
ever published under that name, so there is no compatibility shim: the rename
is a clean break, and this is the list of everything that moved.

If you never ran the old build, you can ignore this file.

## What changed

| | Before | After |
|---|---|---|
| Module | `github.com/nebuloss/claudiquement` | `github.com/nebuloss/claudication` |
| Binary | `claudiquement` | `claudication` |
| Environment | `CLAUDIQ_*` | `CLAUDICATION_*` |
| API key prefix | `claudiq_…` | `clc_…` |
| Session cookie | `claudiq_admin` | `claudication_admin` |
| Database file | `claudiquement.db` | `claudication.db` |
| State directory | `~/.local/state/claudiquement`<br>`/var/lib/claudiquement` | `~/.local/state/claudication`<br>`/var/lib/claudication` |
| systemd unit | `claudiquement.service` | `claudication.service` |
| Container image | `ghcr.io/nebuloss/claudiquement` | `ghcr.io/nebuloss/claudication` |

The environment prefix spells the name out rather than contracting it: there
are six variables, and a shorter prefix saves nothing worth guessing at.

The credential prefix does the opposite and stays short. It is typed into
headers and read back in logs, where `claudication_` would be nine characters
of nothing in front of the part that identifies the key.

## Moving an existing state directory

The database schema is unchanged, so this is a file rename and nothing more.
Stop the gateway first — SQLite in WAL mode leaves two sidecar files, and
moving the database without them loses whatever had not been checkpointed.

```sh
cd "$STATE_DIR"                        # wherever CLAUDIQ_STATE_DIR pointed
mv claudiquement.db     claudication.db
mv claudiquement.db-wal claudication.db-wal 2>/dev/null || true
mv claudiquement.db-shm claudication.db-shm 2>/dev/null || true
```

`secret.key` keeps its name, so the sealed OAuth tokens are readable exactly as
before: your Claude accounts survive, and nothing needs re-authorising.

Then move the directory itself if you were on a default path:

```sh
mv ~/.local/state/claudiquement ~/.local/state/claudication
```

## What does not survive

**API keys have to be re-minted.** The stored hash covers the whole plaintext
including its prefix, so a `claudiq_…` key cannot be rewritten into a `clc_…`
one without the plaintext — which is exactly what is not kept. Old keys stay in
the table and keep authenticating under their old prefix until you remove them;
mint replacements and revoke the originals:

```sh
claudication keys add -name laptop     # hand the new key to the client
claudication keys list                 # then revoke the claudiq_ ones
```

**The admin session ends.** The cookie is named differently and sessions live
in memory anyway, so a restart signs you out regardless. The password itself is
unaffected.

**The theme preference resets.** It is one click in the header.

## systemd

The unit file, its user and group, and the config path all changed together:

```sh
systemctl stop claudiquement
systemctl disable claudiquement
mv /etc/claudiquement /etc/claudication
mv /var/lib/claudiquement /var/lib/claudication
# then install deploy/claudication.service and re-enable
```

`StateDirectory=claudication` in the unit means systemd will create
`/var/lib/claudication` itself, but it will not move what was already there.
