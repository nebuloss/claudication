import { BrandMark, Card, Muted } from '../../primitives'
import { toneContainer, toneFill, type Tone } from '../../styles/tone'
import { IncidentList, UptimeStrips } from '../status'
import { percent, useStatus, type StatusReport, type StatusResult } from '../status/report'

/**
 * The words on the page, apart from how it is drawn.
 *
 * Written for a person, and for one who may have no business here: the page
 * is public, so it says whether the gateway is and has been up, and nothing
 * about what it does. No provider, model, client, path or API is named — each is a fact
 * about this deployment that a passer-by does not need and a scanner would
 * like to have.
 *
 * scripts/check-welcome.mjs fails the build if the bundle grows one of those
 * names, so a later edit here that means well still cannot ship it.
 */
const COPY = {
  title: 'A private AI gateway',
}

type Shown = { tone: Tone; label: string; detail: string }

/** What the headline says, by what /status.json answered and nothing else. */
function shown(r: StatusResult, last: StatusReport | null): Shown {
  if (r.kind === 'checking') return { tone: 'neutral', label: 'Checking…', detail: 'Asking the gateway how it is.' }
  if (r.kind === 'unreachable') {
    return {
      tone: 'error',
      label: 'Not reachable',
      detail:
        last === null
          ? 'This page could not reach the gateway just now. It will try again shortly.'
          : 'This page could not reach the gateway just now; what is below is from its last answer.',
    }
  }
  switch (r.report.state) {
    case 'up':
      return { tone: 'ok', label: 'Accepting requests', detail: 'It is up and answering. If you have a key, you are good to go.' }
    case 'degraded':
      // Accepting, but nothing behind it can take a request right now: a
      // tool that has started failing gets its answer here, without the page
      // saying what is behind it.
      return {
        tone: 'warn',
        label: 'Degraded',
        detail: 'It is up, but cannot serve requests right now. It usually recovers on its own.',
      }
    case 'paused':
      // Switched off by its operator: every request is refused until it is
      // switched back on, and that is deliberate rather than a fault.
      return {
        tone: 'neutral',
        label: 'Paused',
        detail: 'Its operator has switched it off for now. Requests are refused until it is back on.',
      }
  }
}

/**
 * The light by the status: the tone's own fill, except for neutral, where the
 * accent would read as a verdict; the outline is the colour the strips give
 * paused, so the two agree.
 */
const DOT: Record<Tone, string> = { ...toneFill, neutral: 'bg-outline' }

const clock = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' })

function Now({ result, last }: { result: StatusResult; last: StatusReport | null }) {
  const s = shown(result, last)
  const quarter = last?.uptime['90d'] ?? null
  return (
    <Card title="Status">
      {/* A live region, so a screen reader hears the gateway come back
          without having to go looking for it. */}
      <div aria-live="polite" className={`flex items-center gap-4 rounded-md3-l px-5 py-5 ${toneContainer[s.tone]}`}>
        <span className="relative grid size-4 shrink-0 place-items-center" aria-hidden>
          {/* The ripple is the one movement on the page, and only on a good
              answer: it says "live" without a word. Off under reduced
              motion, where the steady dot says the same. */}
          {s.tone === 'ok' && (
            <span className={`absolute inset-0 rounded-full opacity-60 motion-safe:animate-ping ${DOT[s.tone]}`} />
          )}
          <span className={`relative size-3 rounded-full ${DOT[s.tone]}`} />
        </span>
        <span className="text-2xl leading-8">{s.label}</span>
      </div>
      <p className="mt-4 text-sm leading-relaxed text-on-surface">{s.detail}</p>
      {quarter !== null && (
        <p className="mt-6 flex items-baseline gap-2">
          <span className="text-4xl leading-tight tabular-nums text-on-surface">{percent(quarter)}</span>
          <span className="text-sm text-on-surface-variant">uptime over 90 days</span>
        </p>
      )}
      <div className="mt-6 border-t border-outline-variant/40 pt-4">
        <Muted size="xs">
          {result.kind !== 'checking' ? `Checked at ${clock.format(result.at)}. ` : ''}
          Checked again every minute while this page is open.
        </Muted>
      </div>
    </Card>
  )
}

/**
 * The root of a relay that has no admin UI beside it: what the address is, what
 * to do about it, whether it is up now and whether it has been.
 *
 * Reads /status.json and nothing else — aggregated availability, which is all
 * that endpoint carries (see internal/service/uptime) — so the page cannot
 * tell a passer-by more than whether the gateway was running. Not even the
 * build /health carries: a person who landed here has no use for it, and on
 * the page it is one more line for a search for vulnerable releases to match.
 * No link to the admin UI either: this page is only served where the admin is
 * on another address, and naming that address is what splitting the listeners
 * keeps from the public side.
 *
 * Only what someone who opened it can use: is it up, and has it been. The
 * paragraphs that explained the address and told visitors what to do said
 * nothing a person with a key needs, or anyone else can act on, and went.
 * On a desktop the title takes the left and the status the right, with the
 * history across the width below; on a phone they stack.
 */
export default function Welcome() {
  const { result, last } = useStatus()
  return (
    <div className="grid w-full gap-6 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)] lg:gap-8">
      <section aria-labelledby="welcome-title" className="flex flex-col gap-6 py-4 lg:py-8">
        <BrandMark size="lg" />
        <h1 id="welcome-title" className="text-4xl leading-tight font-normal tracking-tight text-on-surface sm:text-5xl">
          {COPY.title}
        </h1>
      </section>
      <div className="lg:self-center">
        <Now result={result} last={last} />
      </div>
      {last !== null && (
        <div className="grid gap-6 lg:col-span-2 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)] lg:gap-8">
          <Card title="History">
            <UptimeStrips report={last} />
          </Card>
          <Card title="Recent incidents">
            <IncidentList report={last} limit={6} />
          </Card>
        </div>
      )}
    </div>
  )
}
