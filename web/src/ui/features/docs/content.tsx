import claudeCodeSh from '#configs/clients/claude-code.sh?raw'
import codexToml from '#configs/clients/codex.toml?raw'
import codexModels from '#configs/clients/codex-models.json?raw'
import crushJson from '#configs/clients/crush.json?raw'
import opencodeJsonc from '#configs/clients/opencode.jsonc?raw'
import { ClientRecipe, type Command, type Section, type Symptom } from './model'

/**
 * What the docs page says, with no idea how any of it is drawn.
 *
 * Separate from the view because they change for unrelated reasons and at
 * unrelated rates: a new client or a newly understood failure is a change
 * here, and nothing in here should have to be read to change a heading level
 * or a column. The config files themselves are not here either — they are the
 * committed ones under configs/clients, imported as text, so there is one copy
 * of each rather than a second set pasted into a component.
 */
export const CLIENTS: ClientRecipe[] = [
  new ClientRecipe({
    id: 'claude-code',
    label: 'Claude Code',
    surface: 'anthropic',
    versioned: false,
    lead: (
      <>
        Claude Code appends <code className="font-mono">/v1</code> to whatever base URL it is
        given, so the address here carries none. Source the file, or put its contents in your
        shell profile.
      </>
    ),
    files: [
      { label: 'claudication.sh', filename: 'claude-code.sh', lang: 'sh', body: claudeCodeSh },
    ],
  }),
  new ClientRecipe({
    id: 'codex',
    label: 'Codex CLI',
    surface: 'openai',
    versioned: true,
    lead: (
      <>
        The only client here that goes through the OpenAI Responses API rather than the Anthropic
        one — so the gateway translates, and Codex never learns it is talking to Claude. Two files:
        the config, and a model catalog Codex reads instead of asking.
      </>
    ),
    files: [
      { label: '~/.codex/config.toml', filename: 'config.toml', lang: 'toml', body: codexToml },
      {
        label: '~/.codex/claude-models.json',
        filename: 'claude-models.json',
        lang: 'json',
        body: codexModels,
      },
    ],
    notes: [
      <>
        Codex needs <code className="font-mono">bubblewrap</code> installed before it can run any
        shell command. Without it the first tool call panics, and the model then explains —
        convincingly — that nothing works.
      </>,
      <>
        The catalog carries a short stand-in for Codex&rsquo;s own system prompt, because Codex
        refuses an entry without one and its real prompt lives inside its binary. The stand-in is
        about nine thousand tokens cheaper per turn, and less good at editing.{' '}
        <code className="font-mono">scripts/codex-model-catalog.py</code> clones the real one out
        of your own Codex if you would rather have that.
      </>,
    ],
  }),
  new ClientRecipe({
    id: 'opencode',
    label: 'opencode',
    surface: 'anthropic',
    versioned: true,
    lead: (
      <>
        This overrides opencode&rsquo;s built-in Anthropic provider rather than declaring a new
        one, so every model the gateway serves is available without listing any of them.
      </>
    ),
    files: [
      {
        label: '~/.config/opencode/opencode.jsonc',
        filename: 'opencode.jsonc',
        lang: 'jsonc',
        body: opencodeJsonc,
      },
    ],
  }),
  new ClientRecipe({
    id: 'crush',
    label: 'crush',
    surface: 'anthropic',
    versioned: false,
    lead: (
      <>
        Every model is spelled out here, and that is deliberate: crush never calls{' '}
        <code className="font-mono">/v1/models</code>, so a model missing from this file cannot be
        selected however well the gateway serves it.
      </>
    ),
    files: [
      {
        label: '~/.config/crush/crush.json',
        filename: 'crush.json',
        lang: 'json',
        body: crushJson,
      },
    ],
  }),
]

/** What goes wrong, and what is actually behind it. */
export const TROUBLESHOOTING: Symptom[] = [
  {
    symptom: '404 on every request',
    cause: (
      <>
        Almost always the <code className="font-mono">/v1</code> question. Claude Code and crush
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
        <code className="font-mono">x-api-key</code> or an{' '}
        <code className="font-mono">Authorization: Bearer</code> token works, on either API.
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

/** Run on the machine hosting the gateway, by whoever runs it. */
export const OPERATOR_COMMANDS: Command[] = [
  {
    cmd: 'claudication keys add -name NAME',
    what: 'Mint a client API key, for a provisioning script.',
  },
  {
    cmd: 'claudication login-url',
    what: 'A single-use link that signs a browser into the admin UI. Spent on first use.',
  },
  { cmd: 'claudication passwd', what: 'Set the admin password. The way back in when it is lost.' },
  {
    cmd: 'claudication backup FILE',
    what: (
      <>
        A consistent snapshot without stopping the service. It holds the sealing key and every
        stored token together, so treat the file as exactly as sensitive as the gateway itself.
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
  { id: 'operator', title: 'For the operator' },
]
