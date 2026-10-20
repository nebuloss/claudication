import { useEffect, useState } from 'react'

/**
 * The names of accounts and clients, by id, for the navigation.
 *
 * The path holds an id; the breadcrumb and the Back button should say the
 * account's email or the client's name. The pages that load an account or a
 * client already have it, so they say its name here and the shell reads it —
 * rather than the shell loading every list again to name one step of a trail.
 * A client's page also says which of its pages it has no use for (a typed
 * client's Models), which the rail then leaves out.
 */
const names = new Map<string, string>()
const omitted = new Map<string, readonly string[]>()
const EVENT = 'claudication:names'

export type ItemKind = 'accounts' | 'clients'

/** Remember what an account or a client is called. */
export function nameItem(kind: ItemKind, id: string, name: string) {
  const key = `${kind}/${id}`
  if (names.get(key) === name) return
  names.set(key, name)
  window.dispatchEvent(new Event(EVENT))
}

/** An account's or a client's name; a short form of its id until it is known. */
export function nameOf(kind: ItemKind, id: string): string {
  return names.get(`${kind}/${id}`) ?? (id.length > 10 ? `${id.slice(0, 8)}…` : id)
}

/** Remember which of an item's pages it has no use for. */
export function omitPages(kind: ItemKind, id: string, pages: readonly string[]) {
  const key = `${kind}/${id}`
  if ((omitted.get(key) ?? []).join() === pages.join()) return
  omitted.set(key, pages)
  window.dispatchEvent(new Event(EVENT))
}

/** The pages an item has said it has no use for. */
export function omittedPages(kind: string, id: string): readonly string[] {
  return omitted.get(`${kind}/${id}`) ?? []
}

/** Re-render when a name becomes known. */
export function useNames(): number {
  const [version, setVersion] = useState(0)
  useEffect(() => {
    const bump = () => setVersion((v) => v + 1)
    window.addEventListener(EVENT, bump)
    return () => window.removeEventListener(EVENT, bump)
  }, [])
  return version
}
