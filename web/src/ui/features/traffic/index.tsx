import TrafficAccounts from './accounts'
import TrafficClients from './clients'
import TrafficModels from './models'
import TrafficRequests from './requests'
import TrafficOverview from './overview'
import type { TrafficPageId } from '../../route'

/**
 * What the gateway has been doing, across every account and client: its load
 * on Traffic's own path, then a page per question (route.ts lists them) —
 * where it went by model, by client and by account, each its traffic over
 * days above its table, and every request in the log. The log keeps its filters in the query string, so a link to a
 * filtered log lands on the log.
 */
export default function Traffic({ page }: { page: TrafficPageId | '' }) {
  switch (page) {
    case '':
      return <TrafficOverview />
    case 'models':
      return <TrafficModels />
    case 'clients':
      return <TrafficClients />
    case 'accounts':
      return <TrafficAccounts />
    case 'requests':
      return <TrafficRequests />
  }
}
