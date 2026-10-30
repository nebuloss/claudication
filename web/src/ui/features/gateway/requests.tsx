import { Card, Stack } from '../../primitives'
import RequestsPanel from '../requests'
import { Outcomes } from './outcomes'

/**
 * Every request: how they turned out over days, then the log of each. The
 * log keeps its filters in the query string, so a link to a filtered log
 * lands here.
 */
export default function GatewayRequests() {
  return (
    <Stack>
      <Outcomes />
      <Card>
        <RequestsPanel title="Log" />
      </Card>
    </Stack>
  )
}
