import { BrandMark, Card, Muted } from '../../primitives'
import { toneContainer, toneFill, type Tone } from '../../styles/tone'
import { REFRESH_MS, useHealth, type HealthState } from './health'

/**
 * The words on the page, apart from how it is drawn.
 *
 * Written for a person, not an integrator, and for one who may have no
 * business here: the page is public, so it says what the address is and what
 * to do about it, and nothing about what the gateway does. No provider, model,
 * client, path or API is named — each is a fact about this deployment that a
 * passer-by does not need and a scanner would like to have. The people who
 * should use it already have a key and their settings from whoever runs it;
 * this page cannot add to that without telling everyone else too.
 *
 * scripts/check-welcome.mjs fails the build if the bundle grows one of those
 * names, so a later edit here that means well still cannot ship it.
 */
const COPY = {
  title: 'A private AI gateway',
  lead: 'This address belongs to a private service, run for a small group of people. There is nothing to browse and nothing to sign in to here.',
  given: {
    title: 'Were you given access?',
    body: 'Use the key and the settings the person who runs it gave you. Your tools talk to this address on their own; you never need to open this page.',
  },
  passing: {
    title: 'Not expecting this page?',
    body: 'Then there is nothing for you to do here. If you think you should have access, ask whoever gave you the address.',
  },
}

type Shown = { tone: Tone; label: string; detail: string; at?: number }

/** What the status says, by what /health answered and nothing else. */
function shown(h: HealthState): Shown {
  switch (h.kind) {
    case 'checking':
      return { tone: 'neutral', label: 'Checking…', detail: 'Asking the gateway how it is.' }
    case 'unreachable':
      return {
        tone: 'error',
        label: 'Not reachable',
        detail: 'This page could not reach the gateway just now. It will try again shortly.',
        at: h.at,
      }
    case 'answered':
      if (!h.ok) {
        return { tone: 'warn', label: 'Not ready', detail: 'It answered, but says it is not ready yet.', at: h.at }
      }
      // Up, but switched off by its operator: every request is refused until
      // it is switched back on, and somebody whose tool has started failing
      // came here to learn exactly that.
      if (!h.accepting) {
        return {
          tone: 'warn',
          label: 'Paused',
          detail: 'It is up, but its operator has switched it off for now. Requests are refused until it is back on.',
          at: h.at,
        }
      }
      return {
        tone: 'ok',
        label: 'Accepting requests',
        detail: 'It is up and answering. If you have a key, you are good to go.',
        at: h.at,
      }
  }
}

/**
 * The light by the status: the tone's own fill, except while checking, when
 * the accent would read as a verdict it has not reached.
 */
const DOT: Record<Tone, string> = { ...toneFill, neutral: 'bg-outline' }

const clock = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' })

function Status() {
  const s = shown(useHealth())
  return (
    <Card title="Status">
      {/* A live region, so a screen reader hears the gateway come back without
          the reader having to go looking for it. */}
      <div aria-live="polite" className={`flex items-center gap-4 rounded-md3-l px-5 py-5 ${toneContainer[s.tone]}`}>
        <span className="relative grid size-4 shrink-0 place-items-center" aria-hidden>
          {/* The ripple is the one movement on the page, and only on a good
              answer: it says "live" without a word. Off under reduced motion,
              where the steady dot says the same. */}
          {s.tone === 'ok' && (
            <span className={`absolute inset-0 rounded-full opacity-60 motion-safe:animate-ping ${DOT[s.tone]}`} />
          )}
          <span className={`relative size-3 rounded-full ${DOT[s.tone]}`} />
        </span>
        <span className="text-2xl leading-8">{s.label}</span>
      </div>
      <p className="mt-4 text-sm leading-relaxed text-on-surface">{s.detail}</p>
      <div className="mt-6 border-t border-outline-variant/40 pt-4">
        <Muted size="xs">
          {s.at !== undefined ? `Checked at ${clock.format(s.at)}. ` : ''}
          Checked again every {REFRESH_MS / 1000} seconds while this page is open.
        </Muted>
      </div>
    </Card>
  )
}

function Guide({ title, body }: { title: string; body: string }) {
  return (
    <Card title={title}>
      <p className="text-sm leading-relaxed text-on-surface-variant">{body}</p>
    </Card>
  )
}

/**
 * The root of a relay that has no admin UI beside it: what the address is, what
 * to do about it, and whether it is up.
 *
 * Reads /health and nothing else — the one endpoint every listener already
 * answers to anyone — so the page cannot tell a passer-by more than a monitor
 * learns. Not even the build /health carries: a person who landed here has no
 * use for it, and printed on the page it is one more line for a search for
 * vulnerable releases to match. No link to the admin UI either: this page is
 * only served where the admin is on another address, and naming that address
 * is what splitting the listeners keeps from the public side.
 *
 * Three blocks on a grid. On a desktop the explanation and its two answers
 * take the left, the status the right; on a phone they stack with the status
 * second, because "is it up" is the question somebody whose tool just failed
 * opened the page to answer, and it should not wait below the advice.
 */
export default function Welcome() {
  return (
    <div className="grid w-full gap-6 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)] lg:gap-8">
      <section aria-labelledby="welcome-title" className="flex flex-col gap-6 py-4 lg:col-start-1 lg:row-start-1 lg:py-8">
        <BrandMark size="lg" />
        <h1 id="welcome-title" className="text-4xl leading-tight font-normal tracking-tight text-on-surface sm:text-5xl">
          {COPY.title}
        </h1>
        <p className="max-w-[60ch] text-lg leading-relaxed text-on-surface-variant">{COPY.lead}</p>
      </section>
      <div className="lg:col-start-2 lg:row-span-2 lg:row-start-1 lg:self-center">
        <Status />
      </div>
      <div className="grid gap-6 sm:grid-cols-2 lg:col-start-1 lg:row-start-2">
        <Guide {...COPY.given} />
        <Guide {...COPY.passing} />
      </div>
    </div>
  )
}
