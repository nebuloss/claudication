import { Fragment, useState, type ReactNode } from 'react'
import type { SlotUse } from '../../../api/client'
import { clientHref } from '../../route'
import { AppLink, InlineMeter, Muted, RowToggle, Table, Td, Tr, TrDetail, type Column } from '../../primitives'
import { ago, compact, smallPercent } from '../../format'
import { providerTitle } from '../../clients/specs'
import { useClientTypes } from '../clients/types'
import { byTokens, rollup, tokensOf, withoutAccount, type Rollup } from './rollup'

/**
 * The usage tables: one implementation of a table of rollups, and the tables
 * built on it — by client, by model name — that the account's, the client's
 * and Gateway's pages all draw with. One, so a figure is formatted, aligned
 * and stacked on a phone the same way wherever it is read, and a fix to one
 * table is a fix to all of them.
 */

const end = (label: string): Column => ({ label, align: 'end' })

/** A column of a usage table beside its figures: a share, a status, a window. */
export type UsageColumn<G> = { head: string | Column; cell: (g: G) => ReactNode; nowrap?: boolean }

/** What a usage table's tokens cell says on demand: every kind, reasoning inside output. */
export function tokenSplit(u: Rollup): string {
  return `${compact(u.input)} in · ${compact(u.output)} out${
    u.reasoning > 0 ? ` (${compact(u.reasoning)} reasoning)` : ''
  } · ${compact(u.cache)} cache`
}

/**
 * The same split as a line, at the top of an unfolded row: a hover is not
 * something a phone has, and the row's own tokens cell gives only the total.
 */
export function TokenSplit({ of: u }: { of: Rollup }) {
  return (
    <div className="mb-2">
      <Muted size="xs">
        {compact(tokensOf(u))} tokens: {tokenSplit(u)}
      </Muted>
    </div>
  )
}

/**
 * A table of rollups, a row each: what it is, the columns its page adds, then
 * the figures every usage table has — requests, failures where there were
 * any, tokens as one compact total with the split on hover, the last request — and
 * unfolded, whatever `detail` puts under it.
 */
export function UsageTable<G extends Rollup>({
  groups,
  name,
  columns = [],
  detail,
}: {
  groups: readonly G[]
  /** The first column: its heading, its cell, and what its toggle says. */
  name: { head: string; cell: (g: G) => ReactNode; label: (g: G) => string }
  columns?: readonly UsageColumn<G>[]
  /** What a row unfolds to; without it, rows do not unfold. */
  detail?: (g: G) => ReactNode
}) {
  const [open, setOpen] = useState<string | null>(null)
  const heads = columns.map((c) => (typeof c.head === 'string' ? { label: c.head } : c.head))
  // A column of failures only when something failed, as SlotTable has it: a
  // column of dashes is a column to read for nothing.
  const failed = groups.some((g) => g.errors > 0)
  const figures = failed ? 4 : 3
  return (
    <Table
      stack
      head={[
        name.head,
        ...heads,
        end('Requests'),
        ...(failed ? [end('Failed')] : []),
        end('Tokens'),
        end('Last request'),
      ]}
    >
      {groups.map((g) => {
        const unfolded = open === g.id
        const toggle = () => setOpen(unfolded ? null : g.id)
        return (
          <Fragment key={g.id}>
            <Tr onClick={detail !== undefined ? toggle : undefined} selected={unfolded}>
              <Td>
                <div className="flex items-center gap-2">
                  {detail !== undefined && (
                    <RowToggle
                      open={unfolded}
                      onToggle={toggle}
                      label={`${unfolded ? 'Hide' : 'Show'} ${name.label(g)}`}
                    />
                  )}
                  <div className="min-w-0">{name.cell(g)}</div>
                </div>
              </Td>
              {columns.map((c, i) => (
                <Td key={heads[i]!.label} label={heads[i]!.label} align={heads[i]!.align} nowrap={c.nowrap}>
                  {c.cell(g)}
                </Td>
              ))}
              <Td label="Requests" nums align="end">
                {compact(g.requests)}
              </Td>
              {failed && (
                <Td label="Failed" nums align="end">
                  {g.errors > 0 ? <span className="text-error">{compact(g.errors)}</span> : <Muted as="span">–</Muted>}
                </Td>
              )}
              <Td label="Tokens" nums align="end" title={tokenSplit(g)}>
                {compact(tokensOf(g))}
              </Td>
              <Td label="Last request" nowrap align="end">
                {ago(g.last)}
              </Td>
            </Tr>
            {unfolded && detail !== undefined && (
              <TrDetail colSpan={1 + heads.length + figures}>{detail(g)}</TrDetail>
            )}
          </Fragment>
        )
      })}
    </Table>
  )
}

/**
 * A part of one window, in points of it: a bar on the window's own 0-100
 * scale, so a client's bar and the window's meter are the same length for the
 * same use, and the clients' bars add up to the window's.
 */
export function Share({ points }: { points: number | undefined }) {
  if (points === undefined || points <= 0) return <Muted as="span">–</Muted>
  return <InlineMeter percent={points} tone="neutral" figure={smallPercent(points)} />
}

/**
 * One window's part of a client: the points, and whose window it is when a
 * column holds several — the gateway's clients on Gateway, against each
 * provider's pool. Named only then: on one account, or one provider, the
 * heading says it all.
 */
export type SharePart = { who?: string; points: number | undefined }

/** A column of shares: its heading, and each client's parts of that window. */
export type ShareColumn = { label: string; parts: (clientId: string) => SharePart[] }

/**
 * A share cell: a bar per window part the client has, one under another, or
 * a dash for none. One column per window whatever the providers, rather than
 * a pair per provider: four columns of mostly dashes pushed the figures off
 * a laptop's screen, and a client rarely draws on more than one.
 */
function ShareCell({ parts }: { parts: SharePart[] }) {
  const used = parts.filter((p) => p.points !== undefined && p.points > 0)
  if (used.length === 0) return <Muted as="span">–</Muted>
  return (
    <div className="flex flex-col items-end gap-1">
      {used.map((p) => (
        <span key={p.who ?? ''} className="inline-flex items-center gap-2">
          {/* A fixed width, so the bars of a column line up whichever
              provider each row names, stacked on a phone as in a table. */}
          {p.who !== undefined && (
            <span className="w-[7ch] truncate text-right text-xs text-on-surface-variant">{p.who}</span>
          )}
          <Share points={p.points} />
        </span>
      ))}
    </div>
  )
}

/** The 5-hour and 7-day columns of one account's shares, read off per client. */
export function accountShareColumns(
  share: (clientId: string) => { five_hour: number; seven_day: number } | undefined,
): ShareColumn[] {
  return [
    { label: '5-hour share', parts: (id) => [{ points: share(id)?.five_hour }] },
    { label: '7-day share', parts: (id) => [{ points: share(id)?.seven_day }] },
  ]
}

/** What a share column is, said once under every table that has one. */
export function ShareNote() {
  return (
    <Muted size="xs">
      A share is an estimate of each window as it stands now, whatever the period above: the
      window&rsquo;s use, split between clients by their cost-weighted part of what this gateway sent
      the account since the window opened. A dash is no traffic in that window.
    </Muted>
  )
}

/** A client's rollup, with what it is called and what it is. */
type ClientUse = Rollup & { name: string; type: string }

/** The model names a client used, the ones it used most first; a real model for a request that named one. */
function slotsOf(rows: readonly SlotUse[]): string {
  return [...new Set(withoutAccount(rows).map((r) => r.slot || r.model))].join(', ')
}

/**
 * Usage by client: an account's own page, and on Gateway the gateway's, a
 * model's and an account's clients. A row each, its share of each window
 * given, its figures, and — where `detail` is on — unfolded to the model
 * names and models it used.
 *
 * The rows may come split by account (the gateway-wide read); they are
 * folded per client here, and per model name under it, so the caller hands
 * over whatever slice it has.
 */
export function ClientTable({
  rows,
  shares = [],
  detail = true,
  provider = true,
  sort = byTokens,
}: {
  rows: readonly SlotUse[]
  shares?: readonly ShareColumn[]
  detail?: boolean
  /** Whether a model says whose it is: not on an account's page, where every one is the account's provider's. */
  provider?: boolean
  sort?: (a: ClientUse, b: ClientUse) => number
}) {
  const { types } = useClientTypes()
  const typeLabel = (t: string) => (t === '' ? 'no longer a client' : (types?.type(t).label ?? t))
  const clients: ClientUse[] = rollup(rows, (r) => r.client_id)
    .map((u) => ({ ...u, name: u.rows[0]!.client || u.id, type: u.rows[0]!.type }))
    .sort(sort)
  return (
    <UsageTable
      groups={clients}
      name={{
        head: 'Client',
        label: (c) => `${c.name}'s models`,
        cell: (c) => (
          <>
            <AppLink href={clientHref(c.id)} strong>
              {c.name}
            </AppLink>
            <div className="truncate text-xs text-on-surface-variant">
              {typeLabel(c.type)} · {slotsOf(c.rows)}
            </div>
          </>
        ),
      }}
      columns={shares.map((s) => ({
        head: end(s.label),
        nowrap: true,
        cell: (c: ClientUse) => <ShareCell parts={s.parts(c.id)} />,
      }))}
      detail={detail ? (c) => <SlotTable rows={withoutAccount(c.rows)} provider={provider} /> : undefined}
    />
  )
}

/**
 * Rows of usage by name and model: a client's own, or one client's unfolded
 * under it.
 *
 * Every figure has a column of its own, right-aligned under a right-aligned
 * heading. Failures and reasoning used to ride in brackets after the request
 * and output counts, which pushed each number off the edge its neighbours sat
 * on; they get columns only when some row has any, so a client that never
 * failed or reasoned is not shown two columns of dashes. `provider` labels
 * each model with whose it is, which an account's page leaves out: there it
 * is every row's.
 */
export function SlotTable({ rows, provider = true }: { rows: SlotUse[]; provider?: boolean }) {
  const failed = rows.some((r) => r.errors > 0)
  const reasoned = rows.some((r) => r.reasoning_tokens > 0)
  return (
    <Table
      stack
      head={[
        'Name',
        'Model',
        end('Requests'),
        ...(failed ? [end('Failed')] : []),
        end('Input'),
        end('Output'),
        ...(reasoned ? [end('Reasoning')] : []),
        end('Cache'),
        end('Last'),
      ]}
    >
      {rows.map((r) => (
        <Tr key={`${r.client_id}/${r.slot}/${r.model}/${r.provider}`}>
          <Td label="Name">
            <code>{r.slot || '—'}</code>
          </Td>
          <Td label="Model">
            <code>{r.model || '—'}</code>
            {provider && (
              <>
                {' '}
                <Muted as="span" size="xs">
                  {providerTitle(r.provider)}
                </Muted>
              </>
            )}
          </Td>
          <Td label="Requests" nums align="end">
            {compact(r.requests)}
          </Td>
          {failed && (
            <Td label="Failed" nums align="end">
              {r.errors > 0 ? <span className="text-error">{compact(r.errors)}</span> : <Muted as="span">–</Muted>}
            </Td>
          )}
          <Td label="Input" nums align="end">
            {compact(r.input_tokens)}
          </Td>
          <Td label="Output" nums align="end">
            {compact(r.output_tokens)}
          </Td>
          {reasoned && (
            // Inside output already, as the provider counts it: shown, never added.
            <Td label="Reasoning" nums align="end">
              {r.reasoning_tokens > 0 ? compact(r.reasoning_tokens) : <Muted as="span">–</Muted>}
            </Td>
          )}
          <Td label="Cache" nums align="end">
            {compact(r.cache_tokens)}
          </Td>
          <Td label="Last" nowrap align="end">
            {ago(r.last)}
          </Td>
        </Tr>
      ))}
    </Table>
  )
}
