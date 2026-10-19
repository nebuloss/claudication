import { type ReactNode } from 'react'
import { api, type DocsInfo } from '../../../api/client'
import { useLoader } from '../../hooks'
import {
  Banner,
  Card,
  Chip,
  CopyField,
  Loading,
  Note,
  Overline,
  Tile,
  railItem,
  tile,
} from '../../primitives'
import { Catalog } from '../../clients/catalog'
import { ClientBody, Prose } from '../../clients/client-body'
import { CLIENTS } from '../../clients/recipes'
import { SECTIONS, TROUBLESHOOTING } from './content'
import { EXAMPLE_BASE, type ClientRecipe } from '../../clients/model'
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
  const { data, error, loading } = useLoader<DocsInfo>((signal) => api.gateway.docsInfo(signal))

  if (loading && data === null) return <Loading />
  if (error !== '' || data === null) {
    return <Banner tone="error">Could not read this gateway&rsquo;s address.</Banner>
  }

  // No fallback to this browser's origin: that is the address of this page,
  // and the page may not be the relay. Pointing a client at it would send
  // every request somewhere that answers 404. Unset means unset, and the files
  // keep the example address with a note saying so.
  const configured = data.public_url ?? ''
  const gateway = configured !== '' ? configured : EXAMPLE_BASE
  const models = data.models
  // The files below are written from this when it could be read, and are the
  // committed ones when it could not.
  const catalog = new Catalog(models)
  const surfaces = data.surfaces ?? []

  return (
    <div className="lg:grid lg:grid-cols-[minmax(0,1fr)_13rem] lg:items-start lg:gap-12">
      <article className="flex min-w-0 max-w-[52rem] flex-col gap-5">
        <header>
          <h2 className="mt-0 mb-2 text-3xl font-normal text-on-surface">Connect a client</h2>
          <p className="m-0 text-base leading-relaxed text-on-surface">
          This gateway relays Claude to any tool that speaks the Anthropic Messages API or the
          OpenAI Responses API. Point one at the address below with a key, and it behaves like the
          real thing — same models, same streaming, same errors.
          </p>
        </header>

        <Section id="before-you-start" title="Before you start">
          {configured === '' && (
            <Note tone="warn" className="mb-4">
              <strong className="font-medium">This gateway has not been told its own public
              address.</strong> The examples below carry a placeholder, so you will have to replace{' '}
              <code>{EXAMPLE_BASE}</code> with the address you reach it on.
            </Note>
          )}
          {!data.ready && (
            <Note tone="warn" className="mb-4">
              <strong className="font-medium">No Claude account is connected yet.</strong> Until
              its operator connects one, this gateway cannot answer a request however a client is
              configured.
            </Note>
          )}

          <Prose>You need two things, and they are the same for every client.</Prose>

          <div className="my-5">
            <CopyField label="Base URL" value={gateway} />
          </div>

          <Prose>
            And an <strong className="font-medium text-on-surface">API key</strong>, which whoever
            runs this gateway issues from its admin UI. One key works on both APIs — nothing needs
            to look like an OpenAI key, and you do not need a Claude account of your own.
          </Prose>

          <Note>
            <strong className="font-medium">
              Clients disagree about <code>/v1</code>.
            </strong>{' '}
            Some append it themselves and want the address without it; others want it included.
            Getting this wrong produces a 404 on every request and no explanation, so each section
            below states the address that client wants.
          </Note>
        </Section>

        {CLIENTS.map((client) => (
          <ClientSection
            key={client.id}
            client={client}
            gateway={gateway}
            surfaces={surfaces}
            catalog={catalog}
          />
        ))}

        <Section id="models" title="Models">
          <Prose>
            Everything this gateway will serve, read from upstream. Clients that ask for the list
            discover these on their own; crush and Codex need them written into their config, and
            the files above are written from this list — so when a model is released, the files
            here have it the next time this page loads.
          </Prose>
          {models.length === 0 ? (
            <Note>
              The model list could not be read just now. The configurations above do not depend on
              it.
            </Note>
          ) : (
            <ul className={`${tile} m-0 flex list-none flex-wrap gap-2 p-3`}>
              {models.map((m) => (
                <li key={m.id} title={m.display_name ?? m.id}>
                  <Chip>
                    <code className="text-on-surface">{m.id}</code>
                  </Chip>
                </li>
              ))}
            </ul>
          )}
        </Section>

        <Section id="troubleshooting" title="Troubleshooting">
          <Prose>
            Most of these are the client rather than the gateway, and most of them arrive with a
            message that points somewhere else.
          </Prose>
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
  catalog,
}: {
  client: ClientRecipe
  gateway: string
  surfaces: DocsInfo['surfaces']
  catalog: Catalog
}) {
  return (
    <Section id={client.id} title={client.label}>
      <ClientBody client={client} gateway={gateway} surfaces={surfaces} catalog={catalog} />
    </Section>
  )
}

/**
 * A section with a heading you can link to, as a card: the admin UI's
 * container, so the two pages read as one product.
 */
function Section({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  return (
    <section id={id} className="scroll-mt-24">
      <Card
        title={
          <a href={`#${id}`} className="group no-underline">
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
        }
      >
        {children}
      </Card>
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
    <dl className="flex flex-col gap-2">
      {items.map((i) => (
        <Tile key={i.key}>
          <dt className="mb-1">{i.term}</dt>
          <dd className="text-sm leading-relaxed text-on-surface">{i.detail}</dd>
        </Tile>
      ))}
    </dl>
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
      // The admin rail's panel: filled, rounded, the section being read in
      // the primary container colour.
      className="order-first mb-5 rounded-md3-xl bg-surface-container p-2 lg:sticky lg:top-24 lg:order-none lg:mb-0"
    >
      <div className="mx-3 my-2">
        <Overline>On this page</Overline>
      </div>
      <ul className="m-0 flex list-none flex-col gap-1 p-0">
        {SECTIONS.map((s) => (
          <li key={s.id}>
            <a
              href={`#${s.id}`}
              // aria-current as well as the colour: the marker is a fact about
              // where you are, and a reader who cannot see the border should
              // not have to infer it from the scroll position.
              aria-current={s.id === current ? 'true' : undefined}
              className={railItem(s.id === current)}
            >
              {s.title}
            </a>
          </li>
        ))}
      </ul>
    </nav>
  )
}
