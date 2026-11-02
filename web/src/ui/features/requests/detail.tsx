import { useEffect, useState, type ReactNode } from 'react'
import { api } from '../../../api/client'
import { millis } from '../../format'
import { Banner, Chip, CopyButton, KeyValue, Modal, Muted, Overline, Stack, Verbatim } from '../../primitives'
import { chatHref } from '../../route'
import { ButtonLink } from '../../primitives'
import { RecordedContent } from './content'
import { RequestRecord } from './record'
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
export function RequestDetail({
  record,
  onClose,
  inChat = false,
}: {
  record: RequestRecord
  onClose: () => void
  /** Opened from its chat's page, which it then need not link to. */
  inChat?: boolean
}) {
  const row = record.row
  // Its place in its chat, where the turns around it are: a chat is a
  // client's, so only for a request that names both.
  const chat =
    !inChat && row.conversation_id !== undefined && row.conversation_id !== '' && row.key_id !== undefined && row.key_id !== ''
      ? chatHref(row.key_id, row.conversation_id, (row.capture ?? 0) > 0 ? 'transcript' : 'requests', row.id)
      : ''
  return (
    // Wider with a recording: its bodies are code, and read best unwrapped.
    <Modal title="Request" onClose={onClose} size={row.capture !== undefined && row.capture > 0 ? 'xl' : 'lg'}>
      <Stack>
        <div className="flex flex-wrap items-center gap-3">
          <StatusChip record={record} />
          <Muted as="span">
            {record.change
              ? "a change the client's agent made through the MCP server"
              : record.refused
                ? 'refused by this gateway'
                : 'forwarded to the upstream'}
          </Muted>
          <span className="flex-grow" />
          {chat !== '' && (
            <ButtonLink href={chat} variant="text" size="sm" icon="chats" navigate>
              Open in its chat
            </ButtonLink>
          )}
          {/* This page with this dialog open, to send or come back to. */}
          <CopyButton label="Copy link" value={requestLink(row.id)} />
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

        {/* Only while its client was being recorded, and only while it is
            held in memory or kept on disk (capture.retention-days). */}
        {row.capture !== undefined && row.capture > 0 && (
          <div>
            <Overline as="div">Content</Overline>
            <div className="mt-2">
              <RecordedContent id={row.capture} />
            </div>
          </div>
        )}
      </Stack>
    </Modal>
  )
}

/** The query parameter a request's dialog is kept in. */
const PARAM = 'request'

function setParam(id: number | null) {
  const u = new URL(window.location.href)
  if (id === null) u.searchParams.delete(PARAM)
  else u.searchParams.set(PARAM, String(id))
  window.history.replaceState(window.history.state, '', u.pathname + u.search + u.hash)
}

/** This page with one request's dialog open: what its Copy link copies. */
export function requestLink(id: number): string {
  const u = new URL(window.location.href)
  u.searchParams.set(PARAM, String(id))
  return u.toString()
}

/**
 * The request a list shows in its dialog, kept in the URL as ?request=<id>, so
 * the dialog can be linked to: a link opens the page with it open, read by its
 * id whether or not it is among the rows the page has, and closing it takes
 * it out of the URL. Replaced rather than pushed, so Back leaves the page
 * rather than closing dialogs one by one.
 */
export function useShownRequest() {
  const [shown, setShown] = useState<RequestRecord | null>(null)
  useEffect(() => {
    const id = Number(new URLSearchParams(window.location.search).get(PARAM) ?? '') || 0
    if (id === 0) return
    const ctl = new AbortController()
    api.traffic.request(id, ctl.signal).then(
      (row) => setShown(new RequestRecord(row)),
      () => {
        // Pruned or never there: the page, without a dialog that cannot open.
        if (!ctl.signal.aborted) setParam(null)
      },
    )
    return () => ctl.abort()
  }, [])
  return {
    shown,
    open: (r: RequestRecord) => {
      setShown(r)
      setParam(r.row.id)
    },
    close: () => {
      setShown(null)
      setParam(null)
    },
  }
}

