import { useState } from 'react'
import {
  api,
  type ApiKey,
} from '../../../api/client'
import { useLoader } from '../../hooks'
import { keyHref, navigate, pathOf } from '../../route'
import {
  Actions,
  AddButton,
  Button,
  Card,
  CopyField,
  Empty,
  ErrorBanner,
  Freshness,
  ListRow,
  ListSummary,
  ListToolbar,
  Loading,
  Modal,
  Note,
  Page,
  RowBadge,
  Stack,
} from '../../primitives'
import { ago, compact } from '../../format'
import { KeyChips, KeyFigures, REFRESH_MS, SUBSCRIPTION_NOTE } from './parts'
import { NewKey } from './limits'

export { KeyPage } from './page'

/**
 * Client API keys — the credential you hand to Claude Code.
 *
 * A key authenticates by its hash, and is kept sealed beside it (migration
 * 026) so its Clients page can show it and its configs again. Keys from
 * before that have only the hash, and say so there.
 */
export default function Keys() {
  const [creating, setCreating] = useState(false)
  // The key just made, shown once over the list: its secret, and the way to
  // its Clients page, where its configurations are.
  const [created, setCreated] = useState<{ key: ApiKey; plaintext: string } | null>(null)
  const { data, error, loading, reload, refreshing, updatedAt, live } = useLoader(
    (signal) => api.keys.list(signal),
    { refreshMs: REFRESH_MS },
  )

  return (
    <Page>
      <ListToolbar
        action={
          <AddButton disabled={creating} onClick={() => setCreating(true)}>
            New API key
          </AddButton>
        }
      >
        {data !== null && data.keys.length > 0 && (
          <ListSummary>traffic over {data.windowDays}d</ListSummary>
        )}
        <Freshness
          updatedAt={updatedAt}
          refreshing={refreshing}
          live={live}
          onRefresh={() => void reload()}
        />
      </ListToolbar>

      {created !== null && (
        <Modal title={`${created.key.name} is ready`} onClose={() => setCreated(null)}>
          <Stack>
            <CopyField label="API key" value={created.plaintext} secret />
            <Note>
              Every client&rsquo;s configuration, with this key already inside, is on its Clients
              page — and stays there, so it can be fetched again whenever a client is set up.
            </Note>
            <Actions end>
              <Button variant="outlined" onClick={() => setCreated(null)}>
                Done
              </Button>
              <Button
                onClick={() => {
                  const to = `${pathOf('keys', created.key.id)}/clients`
                  setCreated(null)
                  navigate(to)
                }}
              >
                Open its clients
              </Button>
            </Actions>
          </Stack>
        </Modal>
      )}

      {creating && (
        <NewKey
          defaultRpm={data?.defaultRpm ?? 0}
          onClose={() => setCreating(false)}
          onCreated={(m) => {
            setCreating(false)
            setCreated(m)
            void reload()
          }}
        />
      )}

      <Card
        info={
          <>
            <p>
              Open a key to change its limits, delete it, or see what it spent and on which
              accounts.
            </p>
            <p>{SUBSCRIPTION_NOTE}</p>
          </>
        }
      >
        <ErrorBanner error={error} className="mb-4" />
        {loading ? (
          <Loading />
        ) : data === null || data.keys.length === 0 ? (
          <Empty>No API keys yet. Create one above and give it to a client.</Empty>
        ) : (
          <Stack gap={2} as="ul">
            {data.keys.map((k) => (
              <li key={k.id}>
                <ListRow
                  href={keyHref(k.id)}
                  leading={
                    <RowBadge>
                      <svg viewBox="0 0 24 24" aria-hidden className="size-4 fill-current">
                        <path d="M12.65 10A6 6 0 1 0 12.65 14H17v4h4v-4h2v-4H12.65zM7 14a2 2 0 1 1 0-4 2 2 0 0 1 0 4z" />
                      </svg>
                    </RowBadge>
                  }
                  title={k.name}
                  subtitle={
                    <>
                      <span className="font-mono">{k.display}</span> · {compact(k.requests)} requests ·{' '}
                      {compact(k.tokens)} tokens over {data.windowDays}d · used {ago(k.last_used_at)}
                    </>
                  }
                  chips={k.managed || k.models_off.length > 0 ? <KeyChips k={k} /> : undefined}
                  meters={<KeyFigures k={k} />}
                />
              </li>
            ))}
          </Stack>
        )}
      </Card>
    </Page>
  )
}
