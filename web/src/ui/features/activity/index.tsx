import ActivityAccounts from './accounts'
import ActivityClients from './clients'
import ActivityModels from './models'
import ActivityRequests from './requests'
import type { ActivityPageId } from '../../route'

/**
 * What the gateway has been doing, across every account and client, a page
 * per question (route.ts lists them): where it went by model, by client and
 * by account, each its traffic over days above its table, and the requests as
 * they happen. The log keeps its filters in the query string, so a link to a
 * filtered log lands on the log.
 */
export default function Activity({ page }: { page: ActivityPageId }) {
  switch (page) {
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
