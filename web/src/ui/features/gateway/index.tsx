import GatewayAccounts from './accounts'
import GatewayClients from './clients'
import GatewayModels from './models'
import GatewayRequests from './requests'
import GatewayOverview from './overview'
import type { GatewayPageId } from '../../route'

/**
 * What the gateway has been doing, across every account and client: its load
 * on Gateway's own path, then a page per question (route.ts lists them) —
 * where it went by model, by client and by account, each its traffic over
 * days above its table, and every request in the log. The log keeps its filters in the query string, so a link to a
 * filtered log lands on the log.
 */
export default function Gateway({ page }: { page: GatewayPageId | '' }) {
  switch (page) {
    case '':
      return <GatewayOverview />
    case 'models':
      return <GatewayModels />
    case 'clients':
      return <GatewayClients />
    case 'accounts':
      return <GatewayAccounts />
    case 'requests':
      return <GatewayRequests />
  }
}
