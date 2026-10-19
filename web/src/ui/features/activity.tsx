import { ChatsPage } from './chats'
import RequestsPanel from './requests'

/**
 * What the gateway has been doing, across every account and key: the
 * conversations (/activity/chats), and the requests they were made of
 * (/activity/requests).
 *
 * One screen for the two because they are the same traffic at two resolutions —
 * a chat's request count links into the log, and a request names its chat —
 * and both are the gateway-wide view that an account's or a key's own Chats
 * and Requests pages narrow. The log keeps its filters in the query string, so
 * a link to a filtered log lands on the log.
 */
export default function Activity({ page }: { page: string }) {
  return page === 'requests' ? (
    <RequestsPanel />
  ) : (
    <ChatsPage />
  )
}
