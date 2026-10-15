import { ChatsPage } from './chats'
import RequestsPanel from './requests'
import { LinkTabs } from '../primitives'
import { ACTIVITY_PAGES } from '../route'

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
export default function Activity({ page, onExpired }: { page: string; onExpired: () => void }) {
  return (
    <div className="flex flex-col gap-5">
      <LinkTabs label="Activity" base="/activity" pages={ACTIVITY_PAGES} current={page} />
      {page === 'requests' ? <RequestsPanel onExpired={onExpired} /> : <ChatsPage onExpired={onExpired} />}
    </div>
  )
}
