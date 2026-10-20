import RequestsPanel from '../requests'
import ActivityAccounts from './accounts'
import ActivityClients from './clients'
import ActivityModels from './models'
import ActivityOverview from './overview'
import type { ActivityPageId } from '../../route'

/**
 * What the gateway has been doing, across every account and client, a page
 * per question (route.ts lists them): its load, where it went by model, by
 * client and by account, and the request log. The log keeps its filters in
 * the query string, so a link to a filtered log lands on the log.
 */
export default function Activity({ page }: { page: ActivityPageId }) {
  switch (page) {
    case 'overview':
      return <ActivityOverview />
    case 'models':
      return <ActivityModels />
    case 'clients':
      return <ActivityClients />
    case 'accounts':
      return <ActivityAccounts />
    case 'requests':
      return <RequestsPanel />
  }
}
