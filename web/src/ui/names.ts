import { useEffect, useState } from 'react'

/**
 * The names of accounts and keys, by id, for the navigation.
 *
 * The path holds an id; the breadcrumb and the Back button should say the
 * account's email or the key's name. The pages that load an account or a key
 * already have it, so they say its name here and the shell reads it — rather
 * than the shell loading every list again to name one step of a trail.
 */
const names = new Map<string, string>()
const EVENT = 'claudication:names'

/** Remember what an account or a key is called. */
export function nameItem(kind: 'accounts' | 'keys', id: string, name: string) {
  const key = `${kind}/${id}`
  if (names.get(key) === name) return
  names.set(key, name)
  window.dispatchEvent(new Event(EVENT))
}

/** An account's or a key's name; a short form of its id until it is known. */
export function nameOf(kind: 'accounts' | 'keys', id: string): string {
  return names.get(`${kind}/${id}`) ?? (id.length > 10 ? `${id.slice(0, 8)}…` : id)
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
