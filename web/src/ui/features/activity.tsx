import { ChatsTab } from './chats'
import RequestsPanel from './requests'
import { usePathItem } from '../hooks'
import { navigate } from '../route'
import { SubNav } from '../primitives'

/** The panels, in the order the sub-nav lists them; each is a path segment. */
const PANELS = ['chats', 'requests'] as const
type Panel = (typeof PANELS)[number]

const LABELS: Record<Panel, string> = { chats: 'Chats', requests: 'Requests' }

/**
 * What the gateway has been doing, across every account and key: the
 * conversations, and the requests they were made of.
 *
 * One tab for the two because they are the same traffic at two resolutions —
 * a chat's request count links into the log, and a request names its chat —
 * and both are the gateway-wide view that an account's or a key's page
 * narrows. The panel is in the path (/activity/requests) rather than the hash
 * because the log keeps its filters in the query string, and a link to a
 * filtered log has to land on the log.
 */
export default function Activity({ onExpired }: { onExpired: () => void }) {
  const item = usePathItem()
  const panel: Panel = (PANELS as readonly string[]).includes(item) ? (item as Panel) : 'chats'

  return (
    <div className="flex flex-col gap-5">
      <SubNav
        label="Activity"
        value={panel}
        onChange={(p) => navigate(`/activity/${p}`)}
        options={PANELS.map((id) => ({ id, label: LABELS[id] }))}
      />
      {panel === 'chats' ? <ChatsTab onExpired={onExpired} /> : <RequestsPanel onExpired={onExpired} />}
    </div>
  )
}
