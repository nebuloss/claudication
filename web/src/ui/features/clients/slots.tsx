import { useState } from 'react'
import { api, type BindingChoice, type SlotWire } from '../../../api/client'
import { useAction } from '../../hooks'
import {
  Actions,
  Button,
  Card,
  Checkbox,
  Chip,
  ErrorBanner,
  Field,
  IconButton,
  Modal,
  Muted,
  Note,
  Select,
  Stack,
  Tile,
} from '../../primitives'
import { Binding, BindingMenu, providerTitle, type Client } from '../../clients/specs'

/**
 * The client's model names: one per model setting its configuration has,
 * each the client's own, each bound to a real model or to a target that
 * moves with each release. Moving the client from Claude to GPT is a change
 * here and nothing else: both APIs reach both providers.
 *
 * The menus offer only what the server says is available to this client now
 * (its `choices`): listed by a connected account, and on there, each model
 * once. Following the newest of a kind is the checkbox beside the menu, not
 * an entry of its own (BindingMenu). A binding that has since become
 * unavailable is kept and shown, with why, until it is replaced.
 */
export function SlotsCard({ client: c, onChanged }: { client: Client; onChanged: () => void }) {
  const choices = c.wire.choices ?? []

  if (!c.namesSlots) {
    return (
      <Card title="Models">
        <Note>
          In direct mode this client names real models: the ones the accounts serve, less those unchecked
          under Allowed models below. Its model names are kept for when it is switched back.
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
          else. When the model picked is the newest of its kind, the box beside it keeps the name on the newest
          as accounts list new releases.
        </p>
      }
    >
      <Stack gap={3}>
        {c.unavailable.length > 0 && (
          <Note tone="warn">
            {c.unavailable.map((s) => s.name).join(', ')} {c.unavailable.length === 1 ? 'is' : 'are'} bound to
            something this client cannot be served now, and {c.unavailable.length === 1 ? 'is' : 'are'} left out
            of its model list. Pick an available model to serve {c.unavailable.length === 1 ? 'it' : 'them'}.
          </Note>
        )}
        {choices.length === 0 && (
          <Note>Nothing is available to bind yet: connect an account, and switch on a model it serves.</Note>
        )}
        {c.slots.map((s) => (
          <SlotRow key={s.id} client={c} slot={s} choices={choices} onChanged={onChanged} />
        ))}
        {c.type.extraSlots && <AddSlot client={c} choices={choices} onChanged={onChanged} />}
      </Stack>
    </Card>
  )
}

/** One name: what it is for, the name itself, and what it is bound to. */
function SlotRow({
  client: c,
  slot: s,
  choices,
  onChanged,
}: {
  client: Client
  slot: SlotWire
  choices: readonly BindingChoice[]
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
  // The current binding is always in the menu, so it says what the slot is
  // bound to — marked, and not choosable, once it is no longer available.
  const menu = new BindingMenu(choices, binding, s)
  const unavailable = s.unavailable ?? ''

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
          <Select label="Serves" value={menu.value} options={menu.options} onChange={(v) => void save(menu.pick(v))}
            disabled={busy !== ''} />
          <FollowBox menu={menu} disabled={busy !== ''} onChange={(b) => void save(b)} />
          {s.removable && (
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
          {unavailable !== '' && <Chip tone="error">unavailable: {unavailable}</Chip>}
        </div>
        <ErrorBanner error={error} />
      </div>
    </Tile>
  )
}

/**
 * A model name of the operator's own: any number of them on a custom client,
 * and on a typed one as many more as its model picker should list beside the
 * names its own settings take.
 *
 * A dialog, as adding a client or an account is: a new name is two choices —
 * what it is called and what it serves — made together, and the list behind
 * it is not what the operator is looking at meanwhile. Created with its
 * binding in one request, so it never exists for a moment bound to a default
 * nobody chose.
 */
function AddSlot({ client: c, choices, onChanged }: { client: Client; choices: readonly BindingChoice[]; onChanged: () => void }) {
  const [open, setOpen] = useState(false)
  return (
    <div>
      <Button variant="outlined" disabled={choices.length === 0} onClick={() => setOpen(true)}>
        Add a model
      </Button>
      {open && (
        <AddSlotDialog
          client={c}
          choices={choices}
          onClose={() => setOpen(false)}
          onAdded={() => {
            setOpen(false)
            onChanged()
          }}
        />
      )}
    </div>
  )
}

function AddSlotDialog({
  client: c,
  choices,
  onClose,
  onAdded,
}: {
  client: Client
  choices: readonly BindingChoice[]
  onClose: () => void
  onAdded: () => void
}) {
  const [name, setName] = useState('')
  // A new name starts following, as the server's own default does: shown as
  // the model it serves now with the box checked.
  const [serves, setServes] = useState(() => BindingMenu.initial(choices))
  const menu = new BindingMenu(choices, serves)
  const { busy, error, run } = useAction()
  const ready = name.trim() !== '' && (serves.model !== '' || serves.follows !== '')
  const add = () =>
    run('add', async () => {
      await api.clients.addSlot(c.id, name.trim(), serves.body)
      onAdded()
    })
  return (
    <Modal title="Add a model" onClose={onClose}>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          if (ready) void add()
        }}
      >
        <Stack gap={4}>
          <Muted size="xs">
            The name is what the client asks for, and what its model picker lists. What it serves can be
            changed later, like any other name&rsquo;s.
          </Muted>
          <Field label="Model name" value={name} onChange={setName} mono autoFocus />
          <div className="flex flex-col gap-1">
            <Select label="Serves" value={menu.value} options={menu.options} onChange={(v) => setServes(menu.pick(v))} />
            <FollowBox menu={menu} onChange={setServes} />
          </div>
          <ErrorBanner error={error} />
          <Actions>
            <Button type="submit" busy={busy === 'add'} disabled={!ready}>
              Add
            </Button>
            <Button variant="text" onClick={onClose}>
              Cancel
            </Button>
          </Actions>
        </Stack>
      </form>
    </Modal>
  )
}

/**
 * The checkbox beside a binding menu, when a target resolves to the model
 * picked: checked, the name follows the target and moves with the next
 * release; unchecked, it stays on this model. Nothing at all for a model no
 * target serves now, so the box never offers what it could not do.
 */
function FollowBox({
  menu,
  onChange,
  disabled,
}: {
  menu: BindingMenu
  onChange: (b: Binding) => void
  disabled?: boolean
}) {
  const f = menu.follow
  if (f === undefined) return null
  return (
    <Checkbox
      checked={f.checked}
      label={f.label}
      disabled={disabled}
      title={`${f.title}: serves this model now, and moves to each newer one as accounts list it`}
      onChange={(on) => onChange(menu.setFollow(on))}
    />
  )
}
