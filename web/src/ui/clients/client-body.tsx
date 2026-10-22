import type { ReactNode } from 'react'
import type { ClientFile } from '../../api/client'
import { CodeViewer, CopyField, Stack } from '../primitives'
import { type ClientRecipe, withKey } from './model'

/** Body prose, at the measure and rhythm of a client's setup. */
export function Prose({ children }: { children: ReactNode }) {
  return <p className="mb-4 text-sm leading-relaxed text-on-surface">{children}</p>
}

/**
 * One client's setup: the address it wants, what is peculiar to its type, its
 * files as the gateway wrote them for it, and the type's notes — what goes
 * wrong with it that no file can fix.
 *
 * `secret` is the key to write into the files, shown as dots; '' leaves the
 * committed placeholder, once the key is no longer offered.
 */
export function ClientBody({
  recipe,
  gateway,
  files,
  secret = '',
}: {
  recipe: ClientRecipe
  gateway: string
  files: ClientFile[]
  secret?: string
}) {
  return (
    <>
      <div className="mb-4">
        <CopyField label="Base URL" value={recipe.baseURL(gateway)} />
      </div>
      <Prose>{recipe.lead}</Prose>
      <div className="mt-5">
        <Stack>
          {files.map((f) => withKey(f, secret)).map((f) => (
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
      {recipe.notes.length > 0 && (
        <div className="mt-5">
          {recipe.notes.map((n, i) => (
            <Prose key={i}>{n}</Prose>
          ))}
        </div>
      )}
    </>
  )
}
