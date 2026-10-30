import { useState } from 'react'
import { api, type BindingChoice, type SlotWire } from '../../../api/client'
import { relative } from '../../format'
import { useAction } from '../../hooks'
import {
  Actions,
  BindingRow,
  Button,
  Card,
  Chip,
  ErrorBanner,
  Field,
  IconButton,
  Modal,
  Muted,
  Note,
  Select,
  Stack,
} from '../../primitives'
import { Binding, BindingMenu, type Client } from '../../clients/specs'

/**
 * The client's model names: one per model setting its configuration has,
 * each the client's own, each bound to a family or to a model, as
 * Settings › Models offers the provider. Moving the client from Claude to GPT
 * is a change here and nothing else: both APIs reach both providers.
 *
 * The menus offer only what the server says is available to this client now
 * (its `choices`): listed by a connected account, and on there, each once
 * (BindingMenu). Which version a family serves is not chosen here: it is the
 * gateway's, on Settings › Models. A binding that has since become
 * unavailable, or was made in the other mode, is kept and shown until it is
 * replaced.
 */
export function SlotsCard({
  client: c,
  onChanged,
  onNamed,
}: {
  client: Client
  onChanged: () => void
  /** A name added, or one renamed to it: what the update command then has to carry. */
  onNamed: (name: string) => void
}) {
  const choices = c.wire.choices ?? []
  const limited = c.slots.filter((s) => s.limited === true && (s.unavailable ?? '') === '')

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
          else. Settings › Models says whether a name is bound to a family (Claude Opus, GPT Sol) or to a model,
          and which version each family serves.
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
        {limited.length > 0 && (
          <Note tone="warn">
            {limited.map((s) => s.name).join(', ')} {limited.length === 1 ? 'is' : 'are'} served by accounts that
            have all reached their usage limit: requests still go to them, and are refused upstream until one has
            room again. Bind {limited.length === 1 ? 'it' : 'them'} to a model another account serves to go on
            working now.
          </Note>
        )}
        {choices.length === 0 && (
          <Note>Nothing is available to bind yet: connect an account, and switch on a model it serves.</Note>
        )}
        {c.slots.map((s) => (
          <SlotRow key={s.id} client={c} slot={s} choices={choices} onChanged={onChanged} onNamed={onNamed} />
        ))}
        {c.type.extraSlots && (
          <AddSlot
            client={c}
            choices={choices}
            onAdded={(name) => {
              onNamed(name)
              // The new name is the client's now: reloaded at once, not at
              // the page's next poll, 20 s later.
              onChanged()
            }}
          />
        )}
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
  onNamed,
}: {
  client: Client
  slot: SlotWire
  choices: readonly BindingChoice[]
  onChanged: () => void
  onNamed: (name: string) => void
}) {
  const [name, setName] = useState(s.name)
  const { busy, error, run } = useAction()
  const binding = Binding.of(s)
  const save = (b: Binding, n = s.name) =>
    run('save', async () => {
      await api.clients.setSlot(c.id, s.id, n, b.body)
      if (n !== s.name) onNamed(n)
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
    <BindingRow
      title={s.label || s.id}
      hint={
        <>
          {s.field !== '' ? <code>{s.field}</code> : null} {s.hint}
        </>
      }
      // What it serves is the menu's own label; under it only why it cannot.
      status={
        unavailable !== '' ? (
          <Chip tone="error">unavailable: {unavailable}</Chip>
        ) : s.limited === true ? (
          <Chip tone="warn">{limitedText(s.back)}</Chip>
        ) : undefined
      }
      error={error}
    >
          <Field label="Model name" value={name} onChange={setName} mono />
          {name !== s.name && (
            <Button variant="outlined" busy={busy === 'save'} disabled={name.trim() === ''}
              onClick={() => void save(binding, name.trim())}>
              Rename
            </Button>
          )}
          <Select label="Serves" value={menu.value} options={menu.options} onChange={(v) => void save(menu.pick(v))}
            disabled={busy !== ''} />
          {s.removable && (
            <IconButton label={`Remove ${s.name}`} onClick={() => void remove()}>
              <path d="M6 19c0 1.1.9 2 2 2h8c1.1 0 2-.9 2-2V7H6v12zM19 4h-3.5l-1-1h-5l-1 1H5v2h14V4z" />
            </IconButton>
          )}
    </BindingRow>
  )
}

/**
 * What a name at its accounts' limit says: that, and when the first is back
 * — an hour or a day off, as the accounts' own quotas read.
 */
export function limitedText(back: string | undefined): string {
  return back !== undefined && back !== '' ? `limit reached · back ${relative(back, 'now')}` : 'limit reached'
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
function AddSlot({ client: c, choices, onAdded }: { client: Client; choices: readonly BindingChoice[]; onAdded: (name: string) => void }) {
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
          onAdded={(name) => {
            setOpen(false)
            onAdded(name)
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
  onAdded: (name: string) => void
}) {
  const [name, setName] = useState('')
  // A new name starts on Claude Opus where it is offered, as the server's
  // own default does.
  const [serves, setServes] = useState(() => BindingMenu.initial(choices))
  const menu = new BindingMenu(choices, serves)
  const { busy, error, run } = useAction()
  const ready = name.trim() !== '' && (serves.model !== '' || serves.follows !== '')
  const add = () =>
    run('add', async () => {
      await api.clients.addSlot(c.id, name.trim(), serves.body)
      onAdded(name.trim())
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
          <Select label="Serves" value={menu.value} options={menu.options} onChange={(v) => setServes(menu.pick(v))} />
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
