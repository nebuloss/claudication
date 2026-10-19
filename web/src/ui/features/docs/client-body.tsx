import type { ReactNode } from 'react'
import type { Surface } from '../../../api/client'
import { CodeViewer, CopyField, Note, Stack } from '../../primitives'
import type { Catalog } from './catalog'
import type { ClientRecipe } from './model'

/** Body prose, at the one measure and rhythm both pages use. */
export function Prose({ children }: { children: ReactNode }) {
  return <p className="mb-4 text-sm leading-relaxed text-on-surface">{children}</p>
}

/**
 * One client's setup: the address it wants, what is peculiar to it, and its
 * files — the inside of a client's card on the public docs page and on a key's
 * Clients page alike. The two drew it separately, and their warnings about a
 * switched-off API had drifted into two wordings of one fact.
 *
 * `secret` is the key to write into the files, shown as dots; '' leaves the
 * committed placeholder, which is what the public page shows.
 */
export function ClientBody({
  client,
  gateway,
  secret = '',
  catalog,
  surfaces,
  notes = true,
}: {
  client: ClientRecipe
  gateway: string
  secret?: string
  catalog: Catalog
  surfaces: Surface[]
  /** The client's own notes, after its files: the docs page's, not a key's. */
  notes?: boolean
}) {
  const missing = client.missingSurfaces(surfaces)
  return (
    <>
      <div className="mb-4">
        <CopyField label="Base URL" value={client.baseURL(gateway)} />
      </div>
      <Prose>{client.lead}</Prose>

      {missing.length > 0 && (
        <Note tone="warn" className="my-4">
          {client.label} needs the {missing.map((s) => s.title).join(' and ')}, which is currently
          switched off on this gateway. Until it is turned back on, this configuration will answer
          404.
        </Note>
      )}

      <div className="mt-5">
        <Stack>
          {client.files(gateway, secret, catalog).map((f) => (
            <CodeViewer
              key={f.filename}
              label={f.label}
              filename={f.filename}
              lang={f.lang}
              value={f.body}
              conceal={secret}
            />
          ))}
        </Stack>
      </div>

      {notes && client.notes.length > 0 && (
        <div className="mt-5">
          {client.notes.map((n, i) => (
            <Prose key={i}>{n}</Prose>
          ))}
        </div>
      )}
    </>
  )
}
