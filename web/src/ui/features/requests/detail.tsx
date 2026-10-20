import type { ReactNode } from 'react'
import { millis } from '../../format'
import { Banner, Chip, CopyButton, KeyValue, Modal, Muted, Overline, Stack, Verbatim } from '../../primitives'
import type { RequestRecord } from './record'
import { StatusChip } from './row'

/** An identifier, in the face that makes one readable. */
function Mono({ children }: { children: ReactNode }) {
  return <code className="text-xs">{children}</code>
}

/**
 * One request, in full.
 *
 * A dialog rather than a panel under the table: the row it belongs to is
 * usually scrolled well out of view by the time anything appears below fifty
 * others, and reading an upstream error means reading all of it — which is the
 * one thing a strip at the bottom of a long list makes hard.
 *
 * Every row opens this, not only the failed ones. A successful request has a
 * record worth reading too — which account served it, what the cache did, how
 * long it took — and the row is the obvious thing to click for it. That is
 * also why the copy here is the whole record rather than the error text: an
 * error pasted into an issue without the model and the account beside it is a
 * sentence with no subject, and a request that worked has no error to paste.
 */
export function RequestDetail({ record, onClose }: { record: RequestRecord; onClose: () => void }) {
  const row = record.row
  return (
    <Modal title="Request" onClose={onClose} size="lg">
      <Stack>
        <div className="flex flex-wrap items-center gap-3">
          <StatusChip record={record} />
          <Muted as="span">
            {record.refused ? 'refused by this gateway' : 'forwarded to the upstream'}
          </Muted>
          <span className="flex-grow" />
          <CopyButton label="Copy record" value={record.asText()} />
        </div>

        {row.error_kind === 'content_check' && (
          <Banner tone="warn">
            <strong>This is a content check, not a quota limit.</strong> The message below talks
            about billing, but adding usage credit will not fix it — the upstream classified this
            request&rsquo;s <em>content</em> as coming from a third-party app rather than from
            Claude Code. The same key usually succeeds on the next request with slightly different
            content, which is what makes it look like a flaky quota problem.
            <br />
            <br />
            Two triggers are known and already rewritten on the way out: a tool named{' '}
            <code>mcp_x</code> instead of <code>mcp__x</code>, and Claude Code&rsquo;s own{' '}
            <code>Is directory a git repo:</code> line inside a foreign system prompt. Seeing this
            anyway means a third trigger — bisect the request body to find it.
          </Banner>
        )}

        <KeyValue
          items={[
            // The exact instant, not "3m ago": this is the value that gets
            // matched against an upstream request_id or somebody else's log.
            ['Timestamp', <span className="tabular-nums">{row.at}</span>],
            ['Outcome', record.outcome],
            ['Model', record.modelLabel],
            ['Chat', <Mono>{record.field('conversation_id')}</Mono>],
            ['Client', record.field('key_name')],
            // With how it was run beside it, where the User-Agent says:
            // Claude Code's Interactive, Headless (claude -p), Agent SDK.
            [
              'Agent',
              row.surface ? (
                <span className="inline-flex flex-wrap items-center gap-2">
                  {record.field('client')}
                  <Chip>{row.surface}</Chip>
                </span>
              ) : (
                record.field('client')
              ),
            ],
            ['Account', record.accountLabel],
            ['IP', <Mono>{record.field('ip')}</Mono>],
            ['Path', <Mono>{row.path}</Mono>],
            ['Tokens', record.tokensLong],
            ['Duration', `${row.duration_ms}ms`],
            ['First token', row.first_token_ms === undefined ? '—' : millis(row.first_token_ms)],
          ]}
        />

        {record.failed && (
          <div>
            <div className="mb-2 flex items-center justify-between gap-4">
              <Overline as="span">What the upstream said</Overline>
              {/* Separate from the record above, because the two get pasted in
                  different places: the record into a message to somebody, this
                  into a search. */}
              <CopyButton label="Copy message" value={row.error ?? ''} />
            </div>
            <Verbatim>{row.error ?? ''}</Verbatim>
          </div>
        )}
      </Stack>
    </Modal>
  )
}
