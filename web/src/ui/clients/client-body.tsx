import type { ReactNode } from 'react'
import { CodeViewer, CopyField, Stack } from '../primitives'
import type { Setup } from './configs'
import type { ClientRecipe } from './model'

/** Body prose, at the measure and rhythm of a client's setup. */
export function Prose({ children }: { children: ReactNode }) {
  return <p className="mb-4 text-sm leading-relaxed text-on-surface">{children}</p>
}

/**
 * One client's setup: the address it wants, what is peculiar to its type, its
 * files, and the type's notes — what goes wrong with it that no file can fix.
 *
 * `secret` is the key to write into the files, shown as dots; '' leaves the
 * committed placeholder, once the key is no longer offered. `setup` writes the
 * files with the client's names; without it they are as committed.
 */
export function ClientBody({
  recipe,
  gateway,
  secret = '',
  setup,
}: {
  recipe: ClientRecipe
  gateway: string
  secret?: string
  setup?: Setup
}) {
  return (
    <>
      <div className="mb-4">
        <CopyField label="Base URL" value={recipe.baseURL(gateway)} />
      </div>
      <Prose>{recipe.lead}</Prose>
      <div className="mt-5">
        <Stack>
          {recipe.files(gateway, secret, setup).map((f) => (
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
