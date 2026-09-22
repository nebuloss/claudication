import claudeCodeSh from '#configs/clients/claude-code.sh?raw'
import codexToml from '#configs/clients/codex.toml?raw'
import codexModels from '#configs/clients/codex-models.json?raw'
import crushJson from '#configs/clients/crush.json?raw'
import opencodeJsonc from '#configs/clients/opencode.jsonc?raw'
import { type ReactNode } from 'react'
import { api, type DocsInfo } from '../../api/client'
import { useLoader } from '../hooks'
import { Banner, CopyField, Spinner } from '../primitives'
import { CodeViewer, type Lang } from '../primitives/code'

/**
 * The example address inside configs/clients/*. Swapping it for this gateway's
 * own is the only thing this page does to those files.
 *
 * They are plain, complete, committed files — readable in the repository,
 * linked from the README, and usable as they stand after one edit. Importing
 * them here as text means there is one copy rather than a second set pasted
 * into a component, and the substitution is a string replace rather than a
 * template language with a renderer at each end.
 */
const EXAMPLE_BASE = 'https://claudication.example.com'

type Client = {
  id: string
  label: string
  /** Which API surface it talks to, so a switched-off one can warn. */
  surface: 'anthropic' | 'openai' | 'both'
  /** Whether the base URL it wants carries /v1. The one thing they disagree on. */
  slash: boolean
  lead: ReactNode
  files: { label: string; filename: string; lang: Lang; body: string }[]
  notes?: ReactNode[]
}

const CLIENTS: Client[] = [
  {
    id: 'claude-code',
    label: 'Claude Code',
    surface: 'anthropic',
    slash: false,
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
  },
  {
    id: 'codex',
    label: 'Codex CLI',
    surface: 'openai',
    slash: true,
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
  },
  {
    id: 'opencode',
    label: 'opencode',
    surface: 'anthropic',
    slash: true,
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
  },
  {
    id: 'crush',
    label: 'crush',
    surface: 'anthropic',
    slash: false,
    lead: (
      <>
        Every model is spelled out here, and that is deliberate: crush never calls{' '}
        <code className="font-mono">/v1/models</code>, so a model missing from this file cannot be
        selected however well the gateway serves it.
      </>
    ),
    files: [
      { label: '~/.config/crush/crush.json', filename: 'crush.json', lang: 'json', body: crushJson },
    ],
  },
]

const TROUBLE: { symptom: string; cause: ReactNode }[] = [
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
    cause: 'That surface has been switched off by whoever runs the gateway. The message names which one.',
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

const SHELL: { cmd: string; what: ReactNode }[] = [
  { cmd: 'claudication keys add -name NAME', what: 'Mint a client API key, for a provisioning script.' },
  { cmd: 'claudication login-url', what: 'A single-use link that signs a browser into the admin UI. Spent on first use.' },
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

/** The sections, in order. One list, so the contents cannot drift from the page. */
const SECTIONS: { id: string; title: string }[] = [
  { id: 'before-you-start', title: 'Before you start' },
  ...CLIENTS.map((c) => ({ id: c.id, title: c.label })),
  { id: 'models', title: 'Models' },
  { id: 'troubleshooting', title: 'Troubleshooting' },
  { id: 'operator', title: 'For the operator' },
]

/**
 * The public documentation: how to point a client at this gateway.
 *
 * Written as a document rather than as a screen. It used to be a tab in the
 * admin UI, where the shape that fits is a stack of cards and one client at a
 * time behind a picker — which is wrong here for two reasons. A reader
 * arriving at documentation scans it before reading any of it, and a picker
 * hides three quarters of the page from that scan; and the section they want
 * is a thing they will link someone else to, which a picker has no address
 * for.
 */
export default function Docs() {
  // One request, and a public one. The in-app version of this screen read
  // /admin/overview, /admin/config and /admin/models — an inventory of the
  // deployment, for four facts. This asks for the four.
  const { data, error, loading } = useLoader<DocsInfo>(() => api.docsInfo())

  if (loading && data === null) {
    return (
      <p className="flex items-center gap-2 text-sm text-on-surface-variant">
        <Spinner /> Loading…
      </p>
    )
  }
  if (error !== '' || data === null) {
    return <Banner tone="error">Could not read this gateway&rsquo;s address.</Banner>
  }

  // No fallback to this browser's origin: that is the address of this page,
  // and the page may not be the relay. Pointing a client at it would send
  // every request somewhere that answers 404. Unset means unset, and the
  // files keep the example address with a note saying so.
  const configured = data.public_url ?? ''
  const base = configured !== '' ? configured : EXAMPLE_BASE
  const models = data.models?.data ?? []
  const surfaces = data.surfaces ?? []
  const offFor = (c: Client) =>
    surfaces.filter((s) => !s.enabled && (c.surface === 'both' || s.id === c.surface))

  return (
    <div className="lg:grid lg:grid-cols-[minmax(0,1fr)_13rem] lg:items-start lg:gap-12">
      <article className="min-w-0 max-w-[46rem]">
        <h1 className="mt-0 mb-3 text-3xl font-semibold tracking-tight text-on-surface">
          Connect a client
        </h1>
        <p className="mt-0 mb-8 text-base leading-relaxed text-on-surface-variant">
          This gateway relays Claude to any tool that speaks the Anthropic Messages API or the
          OpenAI Responses API. Point one at the address below with a key, and it behaves like the
          real thing — same models, same streaming, same errors.
        </p>

        <Section id="before-you-start" title="Before you start">
          {configured === '' && (
            <Banner tone="warn" className="mb-5">
              <strong>This gateway has not been told its own public address.</strong> The examples
              below carry a placeholder, so you will have to replace{' '}
              <code className="font-mono">{EXAMPLE_BASE}</code> with the address you reach it on.
            </Banner>
          )}
          {!data.ready && (
            <Banner tone="warn" className="mb-5">
              <strong>No Claude account is connected yet.</strong> Until its operator connects one,
              this gateway cannot answer a request however a client is configured.
            </Banner>
          )}

          <P>You need two things, and they are the same for every client.</P>

          <div className="my-5">
            <CopyField label="Base URL" value={base} />
          </div>

          <P>
            And an <strong className="font-medium text-on-surface">API key</strong>, which whoever
            runs this gateway issues from its admin UI. One key works on both APIs — nothing needs
            to look like an OpenAI key, and you do not need a Claude account of your own.
          </P>

          <Callout>
            <strong className="font-medium text-on-surface">
              Clients disagree about <code className="font-mono">/v1</code>.
            </strong>{' '}
            Claude Code and crush append it themselves and want the address without it; opencode
            and Codex want it included. Getting this wrong produces a 404 on every request and no
            explanation. Each example below already has it right.
          </Callout>
        </Section>

        {CLIENTS.map((c) => (
          <Section key={c.id} id={c.id} title={c.label}>
            <p className="mt-0 mb-4 text-sm leading-relaxed text-on-surface-variant">
              Base URL{' '}
              <code className="rounded bg-surface-container px-1.5 py-0.5 font-mono text-xs text-on-surface">
                {c.slash ? `${base}/v1` : base}
              </code>
            </p>
            <P>{c.lead}</P>

            {offFor(c).length > 0 && (
              <Banner tone="warn" className="my-4">
                {c.label} needs the {offFor(c).map((s) => s.title).join(' and ')}, which is
                currently switched off on this gateway. Until it is turned back on, this
                configuration will answer 404.
              </Banner>
            )}

            <div className="my-5 flex flex-col gap-4">
              {c.files.map((f) => (
                <CodeViewer
                  key={f.filename}
                  label={f.label}
                  filename={f.filename}
                  lang={f.lang}
                  value={f.body.split(EXAMPLE_BASE).join(base)}
                />
              ))}
            </div>

            {(c.notes ?? []).map((n, i) => (
              <P key={i}>{n}</P>
            ))}
          </Section>
        ))}

        <Section id="models" title="Models">
          <P>
            Everything this gateway will serve, read from upstream. Clients that ask for the list
            discover these on their own; crush and Codex need them written into their config, which
            the examples above do.
          </P>
          {models.length === 0 ? (
            <p className="m-0 text-sm text-on-surface-variant">
              The model list could not be read just now. The configurations above do not depend on
              it.
            </p>
          ) : (
            <ul className="m-0 flex list-none flex-wrap gap-2 p-0">
              {models.map((m) => (
                <li
                  key={m.id}
                  className="rounded-[var(--radius-md3-s)] border border-outline-variant bg-surface-container px-2.5 py-1"
                  title={m.display_name ?? m.id}
                >
                  <code className="font-mono text-xs text-on-surface">{m.id}</code>
                </li>
              ))}
            </ul>
          )}
        </Section>

        <Section id="troubleshooting" title="Troubleshooting">
          <P>
            Most of these are the client rather than the gateway, and most of them arrive with a
            message that points somewhere else.
          </P>
          <dl className="m-0">
            {TROUBLE.map((t) => (
              <div key={t.symptom} className="border-t border-outline-variant py-4 first:border-0">
                <dt className="mb-1.5 text-sm font-semibold text-on-surface">{t.symptom}</dt>
                <dd className="m-0 text-sm leading-relaxed text-on-surface-variant">{t.cause}</dd>
              </div>
            ))}
          </dl>
        </Section>
        <Section id="operator" title="For the operator">
          <P>
            Nothing on this page needs these — they are here because they are the answers to
            questions the rest of it raises. They run on the machine hosting the gateway, where
            shell access to its state directory is already the higher privilege, which is why none
            of them asks for the admin password.
          </P>
          <dl className="m-0">
            {SHELL.map((s) => (
              <div key={s.cmd} className="border-t border-outline-variant py-4 first:border-0">
                <dt className="mb-1.5">
                  <code className="font-mono text-xs text-on-surface">{s.cmd}</code>
                </dt>
                <dd className="m-0 text-sm leading-relaxed text-on-surface-variant">{s.what}</dd>
              </div>
            ))}
          </dl>
        </Section>
      </article>

      <Contents />
    </div>
  )
}

/** A section with a heading you can link to. */
function Section({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  return (
    <section id={id} className="mt-10 scroll-mt-24 border-t border-outline-variant pt-8 first:mt-0">
      <h2 className="group mt-0 mb-4 text-xl font-semibold tracking-tight text-on-surface">
        <a href={`#${id}`} className="no-underline">
          {title}
          {/* The anchor is the point of the heading having an id; shown on
              hover rather than always, because a column of # marks reads as
              decoration and this one is a tool. */}
          <span
            aria-hidden
            className="ml-2 text-on-surface-variant opacity-0 transition-opacity group-hover:opacity-60"
          >
            #
          </span>
        </a>
      </h2>
      {children}
    </section>
  )
}

/** Body prose, at the one measure and rhythm the page uses. */
function P({ children }: { children: ReactNode }) {
  return <p className="mt-0 mb-4 text-sm leading-relaxed text-on-surface-variant">{children}</p>
}

/** An aside that is worth stopping at, without the alarm of a Banner. */
function Callout({ children }: { children: ReactNode }) {
  return (
    <p className="my-5 rounded-[var(--radius-md3-m)] border border-outline-variant border-l-4 border-l-primary bg-surface-container px-4 py-3 text-sm leading-relaxed text-on-surface-variant">
      {children}
    </p>
  )
}

/**
 * On this page.
 *
 * Second in the DOM and first on screen at desktop widths, via the grid order:
 * the document should come first for anything reading the markup rather than
 * looking at it, and a contents list that precedes the title is a thing screen
 * readers have to skip on every visit.
 */
function Contents() {
  return (
    <nav
      aria-label="On this page"
      className="order-first mb-10 lg:sticky lg:top-24 lg:order-none lg:mb-0"
    >
      <p className="mt-0 mb-3 text-xs font-semibold tracking-wide text-on-surface-variant uppercase">
        On this page
      </p>
      <ul className="m-0 list-none border-l border-outline-variant p-0">
        {SECTIONS.map((s) => (
          <li key={s.id}>
            <a
              href={`#${s.id}`}
              className="-ml-px block border-l border-transparent py-1.5 pl-4 text-sm text-on-surface-variant hover:border-l-primary hover:text-on-surface"
            >
              {s.title}
            </a>
          </li>
        ))}
      </ul>
    </nav>
  )
}
