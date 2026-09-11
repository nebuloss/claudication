import { Fragment, useState } from 'react'
import recipes from '#configs/clients.json'
import {
  api,
  type GatewayConfig,
  type Model,
  type Overview as OverviewData,
} from '../../api/client'
import { useLoader } from '../hooks'
import { Banner, Card, CardTitle, CopyField, Spinner, SubNav } from '../primitives'

/**
 * The client recipes come from configs/clients.json, which
 * scripts/gen-client-docs.py also renders into docs/clients.md — and
 * `make check` fails when that file is stale.
 *
 * They used to be written out twice, here and in the docs, which is the same
 * mistake as a hardcoded model list one level up: two copies of a config
 * stanza drift, and the one nobody is looking at is the one that is wrong.
 * Everything below this line is presentation; the content is in that file.
 */
type Recipe = {
  id: string
  label: string
  surface: 'anthropic' | 'openai' | 'both'
  lead: string
  notes: string[]
  snippets: { label: string; lang: string; template: string }[]
  modelEntry?: string
  modelEntryReasoning?: string
  downloads?: { name: string; label: string; what: string }[]
}

const CLIENTS = recipes.clients as Recipe[]

/**
 * Per-model figures the upstream model list does not carry.
 *
 * Only crush needs them — it never calls /v1/models, so every model it can
 * select has to be described in its config file. Getting one wrong changes
 * what crush displays and when it truncates; it changes nothing about what the
 * gateway will serve, which is why a plain fallback is safe for a model that
 * appears after this was written.
 */
function modelFacts(id: string): { context: number; maxTokens: number; reasons: boolean } {
  // The 4.5-era models are the 200k ones; everything newer is a million.
  const small = id.startsWith('claude-opus-4-5') || id.startsWith('claude-haiku-4-5')
  const reasons = !id.startsWith('claude-haiku') && !id.startsWith('claude-sonnet-4-5')
  return {
    context: small ? 200_000 : 1_000_000,
    maxTokens: id.startsWith('claude-opus-5') || id.startsWith('claude-sonnet-5') ? 128_000 : 64_000,
    reasons,
  }
}

/** The capable model to put in an example, and the cheap one. */
function preferred(models: Model[]): string {
  for (const want of ['claude-opus-5', 'claude-sonnet-5', 'claude-fable-5-1']) {
    if (models.some((m) => m.id === want)) return want
  }
  return models[0]?.id ?? recipes.examples.model
}

function smallest(models: Model[]): string {
  return models.find((m) => m.id.startsWith('claude-haiku'))?.id ?? recipes.examples.smallModel
}

/** Substitute {{placeholders}}. Anything unknown is left alone, visibly. */
function render(template: string, vars: Record<string, string>): string {
  return template.replace(/\{\{(\w+)\}\}/g, (whole, key: string) => vars[key] ?? whole)
}

/** crush's model array, one entry per model the gateway serves. */
function crushModels(recipe: Recipe, models: Model[]): string {
  if (recipe.modelEntry === undefined) return ''
  return models
    .map((m) => {
      const f = modelFacts(m.id)
      return render(recipe.modelEntry as string, {
        id: m.id,
        name: m.display_name ?? m.id,
        context: String(f.context),
        maxTokens: String(f.maxTokens),
        reasons: String(f.reasons),
        reasoning: f.reasons ? (recipe.modelEntryReasoning ?? '') : '',
      })
    })
    .join(',\n')
}

/**
 * Prose with `backticks`, rendered as code.
 *
 * The notes are shared with a Markdown document, so they are written in the
 * one piece of Markdown both outputs can honour. Anything richer would mean
 * either a Markdown renderer in the bundle or two copies of the prose, and
 * two copies of the prose is the thing this file exists to stop.
 */
function Prose({ text }: { text: string }) {
  return (
    <>
      {text.split('`').map((part, i) =>
        i % 2 === 1 ? (
          <code key={i}>{part}</code>
        ) : (
          <Fragment key={i}>{part}</Fragment>
        ),
      )}
    </>
  )
}

/**
 * Setup: everything about pointing something at this gateway, in one place.
 *
 * It used to be two halves of two other screens — a card at the bottom of
 * Overview and another at the bottom of Settings — which meant the one job a
 * new user actually arrives to do was split across two tabs, under the ones
 * that administer a gateway they have not connected to yet.
 *
 * Every stanza is filled in with this gateway's own address and its own live
 * model list, so the commonest mistakes cannot be made: every client disagrees
 * about whether the base URL carries `/v1` and none of them says so when you
 * get it wrong, and a model list written by hand starts lying the day an
 * account gains a model.
 */
export default function Setup({
  onExpired,
  onGoTo,
}: {
  onExpired: () => void
  onGoTo: (tab: 'accounts' | 'keys') => void
}) {
  const [client, setClient] = useState<string>(CLIENTS[0].id)
  const { data, error, loading } = useLoader<OverviewData>(() => api.overview(), onExpired)
  // Both loaded separately and both allowed to fail: they are context for the
  // instructions, not the instructions themselves, and neither should be able
  // to blank the screen.
  const { data: config } = useLoader<GatewayConfig>(() => api.config())
  const { data: modelList, error: modelError } = useLoader<{ data: Model[] | null }>(() =>
    api.models(),
  )

  if (loading && data === null) {
    return (
      <p className="flex items-center gap-2 text-sm text-on-surface-variant">
        <Spinner /> Loading…
      </p>
    )
  }
  if (error !== '' || data === null) {
    return <Banner tone="error">Could not read the gateway&rsquo;s address.</Banner>
  }

  // What to tell a client to point at.
  //
  // The origin this browser used is the right answer while one listener serves
  // everything: it is reachable by definition, and better than the bind
  // address, which is often 0.0.0.0 and resolves for nobody.
  //
  // It is the wrong answer once admin-listen splits them. Then this page is on
  // the admin address and the relay is somewhere else — usually a different
  // hostname on a different proxy — and nothing the gateway can see tells it
  // that name. So it is configured, and until it is, this says so rather than
  // handing out a URL that answers 404 to /v1/messages.
  const configured = data.public_url ?? ''
  const unknown = configured === '' && data.admin_split === true
  const base = configured !== '' ? configured : window.location.origin
  const insecure = base.startsWith('http://')

  const models = modelList?.data ?? []
  const recipe = CLIENTS.find((c) => c.id === client) ?? CLIENTS[0]
  const surfaces = config?.surfaces ?? []
  const off = surfaces.filter(
    (s) => !s.enabled && (recipe.surface === 'both' || s.id === recipe.surface),
  )

  const vars: Record<string, string> = {
    base,
    model: preferred(models),
    smallModel: smallest(models),
    models: crushModels(recipe, models),
  }

  return (
    <div className="flex flex-col gap-5">
      <Card>
        <CardTitle>What a client needs</CardTitle>
        <p className="mt-0 mb-4 text-sm text-on-surface">
          Two things, the same for every client: the address below, and one of your{' '}
          <button
            type="button"
            className="text-primary underline underline-offset-2"
            onClick={() => onGoTo('keys')}
          >
            API keys
          </button>
          . One key works on both APIs — nothing needs to look like an OpenAI key.
        </p>

        {!data.ready && (
          <Banner tone="warn" className="mb-4">
            No account is connected yet, so the gateway cannot answer a request however a client is
            configured.{' '}
            <button
              type="button"
              className="underline underline-offset-2"
              onClick={() => onGoTo('accounts')}
            >
              Connect one
            </button>
            .
          </Banner>
        )}

        {unknown && (
          <Banner tone="warn" className="mb-4">
            <strong>This page is not the relay.</strong> The admin UI is on its own listener
            (<code>admin-listen</code>), so the address in your browser serves this page and answers
            404 to <code>/v1/messages</code>. The gateway cannot see the hostname clients reach the
            relay on — set <code>public-url</code> and it will be shown here instead of the guess
            below.
          </Banner>
        )}

        <CopyField label="Base URL" value={base} />

        {insecure && (
          <p className="mt-4 mb-0 text-xs text-on-surface-variant">
            Plain HTTP unless something in front terminates TLS, so the key travels in the clear on
            this network. A tunnel or a reverse proxy is the fix.
          </p>
        )}
      </Card>

      <Card>
        <CardTitle>Models</CardTitle>
        <p className="mt-0 mb-4 text-sm text-on-surface-variant">
          What your connected accounts can serve, read from the upstream just now. The gateway has
          no allowlist of its own — it relays whatever model a client asks for — so anything here
          works on both APIs, and anything absent is a subscription question rather than a gateway
          one.
        </p>
        {modelError !== '' ? (
          <Banner tone="warn">
            Could not reach the upstream model list. The configurations below still work; only this
            list is missing.
          </Banner>
        ) : models.length === 0 ? (
          <p className="m-0 flex items-center gap-2 text-sm text-on-surface-variant">
            <Spinner /> Loading…
          </p>
        ) : (
          <div className="flex flex-wrap gap-2">
            {models.map((m) => (
              <span
                key={m.id}
                className="rounded-[var(--radius-md3-s)] border border-outline bg-surface-high px-2.5 py-1.5"
                title={m.display_name ?? m.id}
              >
                <code className="font-mono text-xs text-on-surface">{m.id}</code>
              </span>
            ))}
          </div>
        )}
      </Card>

      <Card>
        <CardTitle>Configure a client</CardTitle>
        <SubNav
          label="Client"
          value={client}
          onChange={setClient}
          options={CLIENTS.map((c) => ({ id: c.id, label: c.label }))}
        />

        {off.length > 0 && (
          <Banner tone="warn" className="mt-4">
            {recipe.label} needs the {off.map((s) => s.title).join(' and ')}, which is switched off.
            Turn it back on under Settings, or this configuration will answer 404.
          </Banner>
        )}

        <div className="mt-4 flex flex-col gap-4">
          <p className="m-0 text-sm text-on-surface-variant">
            <Prose text={recipe.lead} />
          </p>
          {recipe.snippets.map((s) => (
            <CopyField key={s.label} label={s.label} value={render(s.template, vars)} />
          ))}
          {recipe.notes.map((n) => (
            <p key={n} className="m-0 text-sm text-on-surface-variant">
              <Prose text={n} />
            </p>
          ))}
          {(recipe.downloads ?? []).map((d) => (
            <div
              key={d.name}
              className="rounded-[var(--radius-md3-m)] border border-outline bg-surface-high px-4 py-3"
            >
              <a
                className="font-mono text-xs text-primary underline underline-offset-2"
                href={`/admin/tools/${encodeURIComponent(d.name)}`}
                download={d.name}
              >
                Download {d.label}
              </a>
              <p className="m-0 mt-1 text-sm text-on-surface-variant">
                <Prose text={d.what} />
              </p>
            </div>
          ))}
        </div>
      </Card>

      <Card>
        <CardTitle>From the shell</CardTitle>
        <p className="mt-0 mb-4 text-sm text-on-surface-variant">
          On the machine running the gateway. Shell access to the state directory is already the
          higher privilege, so none of these asks for the admin password.
        </p>
        <dl className="m-0 flex flex-col gap-3">
          {recipes.shell.map((c) => (
            <div
              key={c.cmd}
              className="rounded-[var(--radius-md3-m)] border border-outline bg-surface-high px-4 py-3"
            >
              <dt className="font-mono text-xs text-on-surface">{c.cmd}</dt>
              <dd className="m-0 mt-1 text-sm text-on-surface-variant">
                <Prose text={c.what} />
              </dd>
            </div>
          ))}
        </dl>
      </Card>

      <Card>
        <CardTitle>When it does not work</CardTitle>
        <dl className="m-0 flex flex-col gap-3">
          {recipes.troubleshooting.map((t) => (
            <div
              key={t.symptom}
              className="border-b border-outline-variant pb-3 last:border-0 last:pb-0"
            >
              <dt className="text-sm font-medium text-on-surface">{t.symptom}</dt>
              <dd className="m-0 mt-1 text-sm text-on-surface-variant">
                <Prose text={t.cause} />
              </dd>
            </div>
          ))}
        </dl>
      </Card>
    </div>
  )
}
