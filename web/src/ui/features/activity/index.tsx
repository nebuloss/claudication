import ActivityAccounts from './accounts'
import ActivityClients from './clients'
import ActivityModels from './models'
import ActivityRequests from './requests'
import ActivityOverview from './overview'
import type { ActivityPageId } from '../../route'

/**
 * What the gateway has been doing, across every account and client: its load
 * on Activity's own path, then a page per question (route.ts lists them) —
 * where it went by model, by client and by account, each its traffic over
 * days above its table, and every request in the log. The log keeps its filters in the query string, so a link to a
 * filtered log lands on the log.
 */
export default function Activity({ page }: { page: ActivityPageId | '' }) {
  switch (page) {
    case '':
      return <ActivityOverview />
    case 'models':
      return <ActivityModels />
    case 'clients':
      return <ActivityClients />
    case 'accounts':
      return <ActivityAccounts />
    case 'requests':
      return <ActivityRequests />
  }
}
