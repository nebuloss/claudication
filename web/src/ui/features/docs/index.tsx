import { type ReactNode } from 'react'
import { api, type DocsInfo } from '../../../api/client'
import { useLoader } from '../../hooks'
import { Banner, CopyField, Spinner } from '../../primitives'
import { CodeViewer } from '../../primitives/code'
import { CLIENTS, SECTIONS, TROUBLESHOOTING } from './content'
import { EXAMPLE_BASE, type ClientRecipe } from './model'
import { useCurrentSection } from './use-current-section'

/**
 * The public documentation: how to point a client at this gateway.
 *
 * Written as a document rather than as a screen. It used to be a tab in the
 * admin UI, where the shape that fits is a stack of cards and one client at a
 * time behind a picker — which is wrong here twice over. A reader arriving at
 * documentation scans it before reading any of it, and a picker hides three
 * quarters of the page from that scan; and the section they want is the thing
 * they will send someone else a link to, which a picker has no address for.
 *
 * This file draws. What it says is in ./content, and what a client *is* — the
 * address it wants, the files it needs, the APIs it depends on — is in
 * ./model, where each of those is a question the client answers about itself.
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
  // every request somewhere that answers 404. Unset means unset, and the files
  // keep the example address with a note saying so.
  const configured = data.public_url ?? ''
  const gateway = configured !== '' ? configured : EXAMPLE_BASE
  const models = data.models?.data ?? []
  const surfaces = data.surfaces ?? []

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
            <CopyField label="Base URL" value={gateway} />
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
            Some append it themselves and want the address without it; others want it included.
            Getting this wrong produces a 404 on every request and no explanation, so each section
            below states the address that client wants.
          </Callout>
        </Section>

        {CLIENTS.map((client) => (
          <ClientSection key={client.id} client={client} gateway={gateway} surfaces={surfaces} />
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
          <DefinitionList
            items={TROUBLESHOOTING.map((t) => ({
              key: t.symptom,
              term: <span className="text-sm font-semibold text-on-surface">{t.symptom}</span>,
              detail: t.cause,
            }))}
          />
        </Section>

      </article>

      <Contents />
    </div>
  )
}

/** One client: the address it wants, its files, and anything peculiar to it. */
function ClientSection({
  client,
  gateway,
  surfaces,
}: {
  client: ClientRecipe
  gateway: string
  surfaces: DocsInfo['surfaces']
}) {
  const missing = client.missingSurfaces(surfaces)
  return (
    <Section id={client.id} title={client.label}>
      <p className="mt-0 mb-4 text-sm leading-relaxed text-on-surface-variant">
        Base URL{' '}
        <code className="rounded bg-surface-container px-1.5 py-0.5 font-mono text-xs text-on-surface">
          {client.baseURL(gateway)}
        </code>
      </p>
      <P>{client.lead}</P>

      {missing.length > 0 && (
        <Banner tone="warn" className="my-4">
          {client.label} needs the {missing.map((s) => s.title).join(' and ')}, which is currently
          switched off on this gateway. Until it is turned back on, this configuration will answer
          404.
        </Banner>
      )}

      <div className="my-5 flex flex-col gap-4">
        {client.files(gateway).map((f) => (
          <CodeViewer
            key={f.filename}
            label={f.label}
            filename={f.filename}
            lang={f.lang}
            value={f.body}
          />
        ))}
      </div>

      {client.notes.map((n, i) => (
        <P key={i}>{n}</P>
      ))}
    </Section>
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

/** Term and explanation, for the two lists that are exactly that. */
function DefinitionList({
  items,
}: {
  items: { key: string; term: ReactNode; detail: ReactNode }[]
}) {
  return (
    <dl className="m-0">
      {items.map((i) => (
        <div key={i.key} className="border-t border-outline-variant py-4 first:border-0">
          <dt className="mb-1.5">{i.term}</dt>
          <dd className="m-0 text-sm leading-relaxed text-on-surface-variant">{i.detail}</dd>
        </div>
      ))}
    </dl>
  )
}

/** Body prose, at the one measure and rhythm the page uses. */
function P({ children }: { children: ReactNode }) {
  return <p className="mt-0 mb-4 text-sm leading-relaxed text-on-surface-variant">{children}</p>
}

/** An aside worth stopping at, without the alarm of a Banner. */
function Callout({ children }: { children: ReactNode }) {
  return (
    <p className="my-5 rounded-[var(--radius-md3-m)] border border-outline-variant border-l-4 border-l-primary bg-surface-container px-4 py-3 text-sm leading-relaxed text-on-surface-variant">
      {children}
    </p>
  )
}

/**
 * On this page, marking the section you are reading.
 *
 * Second in the DOM and first on screen at desktop widths, via the grid order:
 * the document should come first for anything reading the markup rather than
 * looking at it, and a contents list that precedes the title is a thing screen
 * readers have to skip on every visit.
 */
function Contents() {
  const current = useCurrentSection(SECTIONS)
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
              // aria-current as well as the colour: the marker is a fact about
              // where you are, and a reader who cannot see the border should
              // not have to infer it from the scroll position.
              aria-current={s.id === current ? 'true' : undefined}
              className={`-ml-px block border-l py-1.5 pl-4 text-sm ${
                s.id === current
                  ? 'border-l-primary font-medium text-primary'
                  : 'border-transparent text-on-surface-variant hover:border-l-outline hover:text-on-surface'
              }`}
            >
              {s.title}
            </a>
          </li>
        ))}
      </ul>
    </nav>
  )
}
