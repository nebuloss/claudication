import { createContext, useContext, useEffect, type ReactNode } from 'react'
import { sessionExpiry, type SessionExpiry } from '../api/client'

/**
 * The session, as the panels see it: somewhere to say it has lapsed.
 *
 * It used to be an onExpired prop, threaded by hand from the shell through
 * every screen to every loader — ten files of it, and still missing from the
 * loaders nobody remembered, whose 401 then showed an error banner with a Retry
 * that could never work instead of the sign-in screen. Now the shell installs
 * one handler, and useLoader and useAction find it here.
 */
const SessionContext = createContext<SessionExpiry>(sessionExpiry)

/**
 * The signed-in part of the app. A lapsed session anywhere below it calls
 * `onExpired`, and only while it is mounted: signed out, a 401 is the answer
 * expected, and there is nothing to sign out of.
 */
export function SessionProvider({
  onExpired,
  children,
}: {
  onExpired: () => void
  children: ReactNode
}) {
  useEffect(() => sessionExpiry.listen(onExpired), [onExpired])
  return <SessionContext.Provider value={sessionExpiry}>{children}</SessionContext.Provider>
}

export function useSession(): SessionExpiry {
  return useContext(SessionContext)
}
