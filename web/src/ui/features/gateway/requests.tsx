import { Card } from '../../primitives'
import RequestsPanel from '../requests'

/**
 * Every request in the log. The load it adds up to is Gateway's own page;
 * the log keeps its filters in the query string, so a link to a filtered log
 * lands here.
 */
export default function GatewayRequests() {
  return (
    <Card>
      <RequestsPanel title="Log" />
    </Card>
  )
}
