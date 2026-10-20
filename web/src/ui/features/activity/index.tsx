import RequestsPanel from '../requests'
import TrafficCard from './traffic-card'
import { Stack } from '../../primitives'
import type { ActivityPageId } from '../../route'

/**
 * What the gateway has been doing, across every account and client: its
 * traffic over time, then the request log, for debugging, filterable by client, provider, account and
 * outcome. Chats are a client's, on its page; totals by provider and by
 * client are the Overview's. The log keeps its filters in the query string,
 * so a link to a filtered log lands on the log.
 */
export default function Activity(_: { page: ActivityPageId }) {
  return (
    <Stack>
      <TrafficCard />
      <RequestsPanel />
    </Stack>
  )
}
