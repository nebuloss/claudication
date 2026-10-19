import type { ReactNode } from 'react'
import { CLIENTS } from '../../clients/recipes'

/**
 * What the docs page says, with no idea how any of it is drawn.
 *
 * Separate from the view because they change for unrelated reasons and at
 * unrelated rates: a new client or a newly understood failure is a change
 * here, and nothing in here should have to be read to change a heading level
 * or a column. The clients themselves are ui/clients/recipes.tsx, shared with a
 * key's Clients page.
 */
/** One thing that goes wrong, and what is actually behind it. */
export type Symptom = { symptom: string; cause: ReactNode }

/** A section of the page, for its heading's anchor and the contents rail. */
export type Section = { id: string; title: string }

/** What goes wrong, and what is actually behind it. */
export const TROUBLESHOOTING: Symptom[] = [
  {
    symptom: '404 on every request',
    cause: (
      <>
        Almost always the <code>/v1</code> question. Claude Code and crush
        want the base URL without it; opencode and Codex want it with. No client says so when it
        is wrong — it just gets nothing.
      </>
    ),
  },
  {
    symptom: '404 saying the API is turned off',
    cause:
      'That surface has been switched off by whoever runs the gateway. The message names which one.',
  },
  {
    symptom: '401 Unauthorized',
    cause: (
      <>
        The key is wrong, withdrawn, or in a header this client does not send. Either{' '}
        <code>x-api-key</code> or an{' '}
        <code>Authorization: Bearer</code> token works, on either API.
      </>
    ),
  },
  {
    symptom: '429 whose message is the single word “Error”',
    cause: (
      <>
        Not a rate limit, despite the status. The subscription backend refuses opus and sonnet to
        anything that is not Claude Code, and says so misleadingly. The gateway handles this for
        you unless the operator has turned that off.
      </>
    ),
  },
  {
    symptom: '“Third-party apps now draw from your extra usage”',
    cause: (
      <>
        Also not what it says: no amount of credit fixes it. A content check refused the request,
        and the same key succeeds on the next one with slightly different content. The gateway
        labels these in its request log.
      </>
    ),
  },
  {
    symptom: 'A model works in one client and not another',
    cause: (
      <>
        The gateway does not filter models, so this is the client. crush needs every model listed
        in its own config, and Codex needs one in its catalog; the others discover them.
      </>
    ),
  },
  {
    symptom: 'Codex: “stream closed before response.completed”',
    cause: (
      <>
        The gateway sends a terminal event on every path, including failures — so this points at
        something between it and Codex, usually a proxy buffering the stream.
      </>
    ),
  },
]

/**
 * The page's sections, in order.
 *
 * Derived from the content rather than written out beside it: the clients in
 * the middle are whatever CLIENTS holds, so adding one puts it in the document
 * and in the contents list and in the scroll tracking, with nothing to keep in
 * step by hand.
 */
export const SECTIONS: Section[] = [
  { id: 'before-you-start', title: 'Before you start' },
  ...CLIENTS.map((c) => ({ id: c.id, title: c.label })),
  { id: 'models', title: 'Models' },
  { id: 'troubleshooting', title: 'Troubleshooting' },
]
