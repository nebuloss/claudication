import {
  type ApiKey,
  type SubscriptionShare,
} from '../../../api/client'
import { accountHref } from '../../route'
import {
  AppLink,
  Card,
  Chip,
  Figure,
  Muted,
  Stat,
  StatGrid,
  Table,
  Td,
  Tr,
} from '../../primitives'
import { compact, smallPercent } from '../../format'
import { periodName } from './limits'

/** The pieces of a key that its list and its pages both show. */

/**
 * Keys change when someone mints one, but "last used" and the spent budget
 * move with traffic, and the budget is the figure an operator watches when
 * they are deciding whether a limit is set too low.
 */
export const REFRESH_MS = 20_000

/**
 * Why the gateway's own key has no working controls.
 *
 * Deleting it never did what the button implied — the next chat needing a name
 * issued a fresh one under a new id and orphaned the usage attributed to the
 * old one — and editing it is the operator changing limits on something they
 * did not create and do not call. The control that governs what it spends is
 * the switch that turns the feature on.
 */
export const MANAGED_HINT =
  'Issued by the gateway for its own requests. Turn off chat names in Settings to stop it.'

/** What sets a key apart, as chips: the gateway's own, or models switched off. */
export function KeyChips({ k }: { k: ApiKey }) {
  return (
    <>
      {k.managed && <Chip tone="neutral">gateway&rsquo;s own</Chip>}
      {k.models_off.length > 0 && (
        <Chip tone="warn">
          {k.models_off.length} model{k.models_off.length === 1 ? '' : 's'} off
        </Chip>
      )}
    </>
  )
}

/**
 * A key's limits and spend, as one row of figures: the same in the list and
 * on the key's own page, so the two cannot disagree about it.
 */
export function KeyFigures({ k, always = true }: { k: ApiKey; always?: boolean }) {
  return (
    <>
      <Figure label="Rate">
        {k.rpm_limit > 0 ? `${compact(k.rpm_limit)} / ${periodName(k.rate_period_s)}` : 'default'}
      </Figure>
      <Figure label="Today">
        <Budget budget={k.token_budget} spent={k.spent_today} />
      </Figure>
      {/* A dash keeps the list's figures in step; alone in a page's header it
          would say nothing, so there it is left out. */}
      {(always || k.subscription !== undefined) && <SubscriptionCell share={k.subscription} />}
    </>
  )
}

/** What the subscription figures are, said once wherever they appear. */
export const SUBSCRIPTION_NOTE =
  'Subscription is an estimate: each account’s 5-hour and 7-day utilisation, split ' +
  'between keys by their cost-weighted share of what this gateway sent it, as a share of ' +
  'every account’s capacity together. Anything else using an account is counted against ' +
  'the keys that used it here.'

/** A key's estimated part of the subscription, both windows, for a table cell. */
export function SubscriptionCell({ share }: { share: SubscriptionShare | undefined }) {
  if (share === undefined) return <Muted as="span" size="xs">—</Muted>
  return (
    <span className="whitespace-nowrap text-xs tabular-nums text-on-surface">
      <span className="text-on-surface-variant">5h</span> {smallPercent(share.five_hour)}
      <span className="mx-1.5 text-on-surface-variant">·</span>
      <span className="text-on-surface-variant">7d</span> {smallPercent(share.seven_day)}
    </span>
  )
}

/**
 * The same estimate on a key's page, account by account: how many points of
 * each account's windows this key accounts for, and its share of what the
 * gateway sent that account.
 */
export function SubscriptionCard({ share }: { share: SubscriptionShare }) {
  return (
    <Card title="Share of the Claude subscription" info={<p>{SUBSCRIPTION_NOTE}</p>}>
      <StatGrid>
        <Stat label="5-hour" value={smallPercent(share.five_hour)} hint="of all accounts" />
        <Stat label="7-day" value={smallPercent(share.seven_day)} hint="of all accounts" />
      </StatGrid>
      {share.accounts.length > 0 && (
        <div className="mt-4">
          <Table head={['Account', '5-hour window', '7-day window']}>
            {share.accounts.map((a) => (
              <Tr key={a.account_id}>
                <Td>
                  <AppLink href={accountHref(a.account_id)} strong>
                    {a.email}
                  </AppLink>
                </Td>
                <Td nums>
                  {smallPercent(a.five_hour)}{' '}
                  <Muted as="span" size="xs">
                    ({Math.round(a.five_hour_share * 100)}% of its traffic here)
                  </Muted>
                </Td>
                <Td nums>
                  {smallPercent(a.seven_day)}{' '}
                  <Muted as="span" size="xs">
                    ({Math.round(a.seven_day_share * 100)}% of its traffic here)
                  </Muted>
                </Td>
              </Tr>
            ))}
          </Table>
        </div>
      )}
    </Card>
  )
}

/**
 * A key's daily token ceiling, and how close it is to it.
 *
 * "Unlimited" spelled out rather than shown as 0, which reads as the opposite
 * of what it means. When there is a budget the spend is shown against it, since
 * "is this key about to be cut off" is the question the column exists for.
 */
export function Budget({ budget, spent }: { budget: number; spent: number }) {
  if (budget <= 0) return <Muted as="span" size="xs">unlimited</Muted>
  const share = spent / budget
  const tone =
    share >= 1 ? 'text-error' : share >= 0.8 ? 'text-on-warning-container' : 'text-on-surface'
  return (
    <span className={`tabular-nums ${tone}`}>
      {compact(spent)} / {compact(budget)}
    </span>
  )
}
