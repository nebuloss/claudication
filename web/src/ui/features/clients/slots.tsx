import { useState } from 'react'
import { api, type SlotWire } from '../../../api/client'
import { useAction, useLoader } from '../../hooks'
import {
  Button,
  Card,
  Chip,
  ErrorBanner,
  Field,
  IconButton,
  Muted,
  Note,
  Select,
  Stack,
  Tile,
} from '../../primitives'
import { Binding, bindingOptions, providerTitle, type Client, type ClientTypes } from '../../clients/specs'

/**
 * The client's model names: one per model setting its configuration has,
 * each the client's own, each bound to a real model of either provider or to
 * a target that moves with each release. Moving the client from Claude to GPT
 * is a change here and nothing else — for a client on an API that reaches
 * both. A binding its API cannot reach is allowed and said: the gateway
 * refuses those requests until the binding or the API changes.
 */
export function SlotsCard({ client: c, types, onChanged }: { client: Client; types: ClientTypes; onChanged: () => void }) {
  const models = useLoader((signal) => api.models.list(signal))
  const options = bindingOptions(models.data ?? [], types.follows, c.dialect)

  if (!c.namesSlots) {
    return (
      <Card title="Models">
        <Note>
          In direct mode this client names real models: the ones the accounts serve, less those switched off
          on its Models page. Its model names below are kept for when it is switched back.
        </Note>
      </Card>
    )
  }
  return (
    <Card
      title="Model names"
      info={
        <p>
          A name is this client&rsquo;s alone: another client may have one by the same name, bound to something
          else. Following a target serves the newest model of its kind as accounts list it.
        </p>
      }
    >
      <Stack gap={3}>
        {c.unreachable.length > 0 && (
          <Note tone="warn">
            {c.unreachable.map((s) => s.name).join(', ')} {c.unreachable.length === 1 ? 'is' : 'are'} bound to a{' '}
            {providerTitle(c.unreachable[0]?.provider)} model, which the {types.dialectTitle(c.dialect)} API this
            client is pointed at cannot reach: those requests are refused.
            {c.type.speaks('openai') && c.dialect !== 'openai' && ' Point the client at the OpenAI Responses API to serve it.'}
          </Note>
        )}
        {c.slots.map((s) => (
          <SlotRow key={s.id} client={c} slot={s} options={options} onChanged={onChanged} />
        ))}
        {c.type.custom && <AddSlot client={c} onChanged={onChanged} />}
      </Stack>
    </Card>
  )
}

/** One name: what it is for, the name itself, and what it is bound to. */
function SlotRow({
  client: c,
  slot: s,
  options,
  onChanged,
}: {
  client: Client
  slot: SlotWire
  options: ReturnType<typeof bindingOptions>
  onChanged: () => void
}) {
  const [name, setName] = useState(s.name)
  const { busy, error, run } = useAction()
  const binding = Binding.of(s)
  const save = (b: Binding, n = s.name) =>
    run('save', async () => {
      await api.clients.setSlot(c.id, s.id, n, b.body)
      onChanged()
    })
  const remove = () =>
    run('remove', async () => {
      await api.clients.removeSlot(c.id, s.id)
      onChanged()
    })
  // The current binding is always one of the choices, even a model no
  // account lists any more, so the menu says what the slot is bound to.
  const all = options.some((o) => o.value === binding.value)
    ? options
    : [{ value: binding.value, label: binding.model || binding.follows, group: 'Bound now' }, ...options]
  const misfit = !c.type.fits(c.dialect, name)

  return (
    <Tile>
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <span className="text-sm font-medium text-on-surface">{s.label || s.id}</span>
          <Muted as="span" size="xs">
            {s.field !== '' ? <code>{s.field}</code> : null} {s.hint}
          </Muted>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <Field label="Model name" value={name} onChange={setName} mono />
          {name !== s.name && (
            <Button variant="outlined" busy={busy === 'save'} disabled={name.trim() === ''}
              onClick={() => void save(binding, name.trim())}>
              Rename
            </Button>
          )}
          <Select label="Serves" value={binding.value} options={all} onChange={(v) => void save(Binding.parse(v))}
            disabled={busy !== ''} />
          {c.type.custom && (
            <IconButton label={`Remove ${s.name}`} onClick={() => void remove()}>
              <path d="M6 19c0 1.1.9 2 2 2h8c1.1 0 2-.9 2-2V7H6v12zM19 4h-3.5l-1-1h-5l-1 1H5v2h14V4z" />
            </IconButton>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2 text-xs text-on-surface-variant">
          <span>
            now <code className="text-on-surface">{s.serving || '—'}</code>
            {s.provider !== undefined && ` on ${providerTitle(s.provider)}`}
          </span>
          {s.unreachable === true && <Chip tone="warn">not on this client&rsquo;s API</Chip>}
          {(s.problem ?? '') !== '' && <Chip tone="error">{s.problem}</Chip>}
          {misfit && <Chip tone="warn">needs “{c.type.mark(c.dialect)}” in the name on this API</Chip>}
        </div>
        <ErrorBanner error={error} />
      </div>
    </Tile>
  )
}

/** A custom client's own new name, following the newest Claude Opus until bound. */
function AddSlot({ client: c, onChanged }: { client: Client; onChanged: () => void }) {
  const [name, setName] = useState('')
  const { busy, error, run } = useAction()
  const add = () =>
    run('add', async () => {
      await api.clients.addSlot(c.id, name.trim())
      setName('')
      onChanged()
    })
  return (
    <div className="flex flex-wrap items-center gap-3">
      <Field label="New model name" value={name} onChange={setName} mono />
      <Button variant="outlined" busy={busy === 'add'} disabled={name.trim() === ''} onClick={() => void add()}>
        Add
      </Button>
      <ErrorBanner error={error} />
    </div>
  )
}
