/**
 * The kinds of client the gateway configures, and a client as one of them.
 *
 * The table itself is the server's (internal/service/clients, served at
 * /admin/client-types): every type's slots, labels, default names and
 * targets, and the API it speaks. These classes only read
 * it, so a slot's label here and in the store cannot disagree. Pure: no
 * React, so `make test-web` exercises it.
 */

import type {
  BindingChoice,
  ClientTypesWire,
  ClientWire,
  DialectId,
  SlotWire,
  TypeSpec,
} from '../../api/types'

/** What each provider is called on a screen. */
export const PROVIDER_TITLES: Record<string, string> = { anthropic: 'Claude', openai: 'ChatGPT' }

/** A provider's name, or the id itself for one this UI does not know. */
export function providerTitle(id: string | undefined): string {
  return PROVIDER_TITLES[id ?? 'anthropic'] ?? id ?? ''
}

/** One kind of client. */
export class ClientType {
  constructor(private readonly spec: TypeSpec) {}

  get id(): string {
    return this.spec.id
  }

  get label(): string {
    return this.spec.label
  }

  get custom(): boolean {
    return this.spec.custom
  }

  /** Takes model names of the operator's own beside its own slots: a custom client, and every typed one for its picker. */
  get extraSlots(): boolean {
    return (this.spec.extra_slots ?? '') !== ''
  }

  get slots(): TypeSpec['slots'] {
    return this.spec.slots
  }


  get namesItsChats(): boolean {
    return this.spec.names_its_chats
  }

  /** The API it speaks, fixed by the type; undefined for a custom client, whose key works on both. */
  get api(): DialectId | undefined {
    return this.spec.api
  }

  /** A slot of this type, by id. */
  slot(id: string): TypeSpec['slots'][number] | undefined {
    return this.spec.slots.find((s) => s.id === id)
  }
}

/** The server's table, with lookups over it. */
export class ClientTypes {
  readonly all: ClientType[]

  constructor(private readonly wire: ClientTypesWire) {
    this.all = wire.types.map((t) => new ClientType(t))
  }

  /** A type by id; custom for one this UI has never heard of. */
  type(id: string): ClientType {
    return this.all.find((t) => t.id === id) ?? this.all.find((t) => t.custom) ?? new ClientType(FALLBACK)
  }

  /** Where the gateway's MCP server is on the relay, as the server says. */
  get mcpPath(): string {
    return this.wire.mcp_path
  }

  /** How an API is named. */
  dialectTitle(d: DialectId): string {
    return this.wire.dialects.find((x) => x.id === d)?.title ?? d
  }

  /** The API a type speaks, named; both for a custom client. */
  apiTitle(t: ClientType): string {
    return t.api !== undefined ? this.dialectTitle(t.api) : 'both APIs'
  }
}

const FALLBACK: TypeSpec = {
  id: 'custom', label: 'Custom', slots: [], custom: true, names_its_chats: false,
}

/**
 * What a slot is bound to, as a Select holds it: a real model ("model:<id>")
 * or a target it follows ("follow:<target>"). One string, so a menu can list
 * both kinds side by side.
 */
export class Binding {
  private constructor(
    readonly model: string,
    readonly follows: string,
  ) {}

  static model(id: string): Binding {
    return new Binding(id, '')
  }

  static follow(target: string): Binding {
    return new Binding('', target)
  }

  /** A slot's current binding. */
  static of(slot: Pick<SlotWire, 'model' | 'bound'>): Binding {
    return slot.model !== '' ? Binding.model(slot.model) : Binding.follow(slot.bound ?? '')
  }

  /** A Select's value back; an unreadable one follows nothing. */
  static parse(value: string): Binding {
    if (value.startsWith('model:')) return Binding.model(value.slice(6))
    if (value.startsWith('follow:')) return Binding.follow(value.slice(7))
    return Binding.follow('')
  }

  get value(): string {
    return this.model !== '' ? `model:${this.model}` : `follow:${this.follows}`
  }

  /** As the admin API takes it. */
  get body(): { model?: string; follows?: string } {
    return this.model !== '' ? { model: this.model } : { follows: this.follows }
  }
}

/** One choice in a binding menu: what it is, grouped by whose it is. */
export interface BindingOption {
  value: string
  label: string
  hint?: string
  group: string
  disabled?: boolean
}

/** What a slot's binding menu reads off the slot, beyond the binding itself. */
export type BoundSlot = Pick<SlotWire, 'title' | 'serving' | 'unavailable'>

/**
 * The checkbox beside a binding menu: the picked model is what `follows`
 * resolves to now, and checking it binds the target instead of the model.
 */
export interface FollowOffer {
  follows: string
  /** The checkbox's own words. */
  label: string
  /** The target's name, for the hover text. */
  title: string
  checked: boolean
}

/**
 * How the checkbox words following a target: what it moves with, short
 * enough to sit beside the menu. The target's own title ("Newest Claude
 * Opus") names the model a second time, which is what the menu stopped
 * doing.
 */
export function followLabel(follows: string, title = ''): string {
  if (follows === 'openai:first') return "Follow ChatGPT's top model"
  if (follows === 'openai:light') return "Follow ChatGPT's light model"
  const family = follows.startsWith('claude:') ? follows.slice(7) : ''
  if (family !== '') return `Follow new ${family.charAt(0).toUpperCase()}${family.slice(1)} releases`
  return `Follow ${title || follows}`
}

/**
 * A slot's binding as one menu of models and a checkbox beside it.
 *
 * The menu lists each available model once, by provider: a target that
 * follows the newest of a kind is no entry of its own, because it always
 * serves one of the models already listed, and the owner found the menu
 * naming that model twice ("Newest Claude Opus — now claude-opus-5-5" over
 * Claude Opus 5.5). Following is instead a property of the model picked: when
 * a target resolves to it now (the server's `followed_by`), the checkbox
 * offers to bind the target, which serves the same model today and moves on
 * with the next release. A slot bound to a target shows the model it resolves
 * to, the box checked.
 *
 * Only the server's choices, which are only what is available to the client:
 * nothing here is filtered or guessed. A slot bound to something no longer
 * available keeps it, and the menu shows it first, marked and not choosable
 * again, with no checkbox, so the operator sees what it is and can only
 * replace it with something that works.
 */
export class BindingMenu {
  readonly options: BindingOption[]
  /** The Select's value: the model served, or the binding itself when unavailable. */
  readonly value: string
  /** The checkbox, when a target resolves to the model picked; none otherwise. */
  readonly follow?: FollowOffer
  private readonly picked?: BindingChoice

  constructor(
    private readonly choices: readonly BindingChoice[],
    readonly binding: Binding,
    current?: BoundSlot,
  ) {
    const models = choices.filter((c) => (c.model ?? '') !== '')
    this.options = models.map((c) => ({
      value: Binding.model(c.model ?? '').value,
      label: c.title || (c.model ?? ''),
      hint: c.model,
      group: providerTitle(c.provider),
    }))
    this.picked = binding.model !== ''
      ? models.find((c) => c.model === binding.model)
      : (models.find((c) => (c.followed_by ?? []).some((t) => t.follows === binding.follows)) ??
        // A target the server resolved for the slot but not among the menu's
        // own (a family it no longer lists first): still what it serves.
        (current !== undefined && (current.unavailable ?? '') === '' && current.serving !== ''
          ? models.find((c) => c.model === current.serving)
          : undefined))
    if (this.picked === undefined) {
      this.value = binding.value
      if (current !== undefined) {
        this.options.unshift({
          value: binding.value,
          label: current.title || binding.model || binding.follows,
          hint: (current.unavailable ?? '') !== '' ? `unavailable: ${current.unavailable}` : 'unavailable',
          group: 'Bound now',
          disabled: true,
        })
      }
      return
    }
    this.value = Binding.model(this.picked.model ?? '').value
    const targets = this.picked.followed_by ?? []
    const t = binding.follows !== ''
      ? (targets.find((x) => x.follows === binding.follows) ?? { follows: binding.follows, title: current?.title ?? '' })
      : targets[0]
    if (t !== undefined) {
      this.follow = { follows: t.follows, title: t.title, label: followLabel(t.follows, t.title), checked: binding.follows !== '' }
    }
  }

  /**
   * What picking a model in the menu binds. The checkbox is kept as it
   * stood: following, the new model is followed too when a target serves
   * it, and bound as itself when none does.
   */
  pick(value: string): Binding {
    const b = Binding.parse(value)
    if (b.model === '' || this.follow?.checked !== true) return b
    const t = this.choices.find((c) => c.model === b.model)?.followed_by?.[0]
    return t !== undefined ? Binding.follow(t.follows) : b
  }

  /**
   * What the checkbox binds: the target, or the model it serves now — the
   * same model today either way, so nothing served changes until the next
   * release.
   */
  setFollow(on: boolean): Binding {
    if (this.follow === undefined || this.picked === undefined) return this.binding
    return on ? Binding.follow(this.follow.follows) : Binding.model(this.picked.model ?? '')
  }

  /**
   * What a new name starts on: following the newest Claude Opus, as the
   * server's own default and every type's main name do, when it serves
   * something; otherwise the first target the menu has, or else its first
   * model.
   */
  static initial(choices: readonly BindingChoice[]): Binding {
    const targets = choices.flatMap((c) => c.followed_by ?? [])
    const t = targets.find((x) => x.follows === 'claude:opus') ?? targets[0]
    if (t !== undefined) return Binding.follow(t.follows)
    const m = choices.find((c) => (c.model ?? '') !== '')
    return m !== undefined ? Binding.model(m.model ?? '') : Binding.follow('')
  }
}

/** A client, as its page reads it. */
export class Client {
  constructor(
    readonly wire: ClientWire,
    readonly type: ClientType,
  ) {}

  get id(): string {
    return this.wire.id
  }

  get name(): string {
    return this.wire.name
  }

  /** The API it speaks: its type's. A custom client's key works on both, and has none. */
  get api(): DialectId | undefined {
    return this.type.api
  }

  get slots(): SlotWire[] {
    return this.wire.slots
  }

  /** Names its own slots: every typed client, a custom one in binding mode. */
  get namesSlots(): boolean {
    return !this.type.custom || this.wire.mode === 'binding'
  }

  /** Set up with its key: from then on the key and the install command are not offered. */
  get configured(): boolean {
    return this.wire.configured_at !== undefined
  }

  /** Has files to write and an install command. */
  get installs(): boolean {
    return !this.type.custom
  }

  /** The slots bound to something not available to it now, for the warnings. */
  get unavailable(): SlotWire[] {
    return this.wire.slots.filter((s) => (s.unavailable ?? '') !== '')
  }

  /** One line for a list row: each slot's name and what it serves. */
  get summary(): string {
    if (!this.namesSlots) return 'real model ids'
    if (this.wire.slots.length === 0) return 'no model names yet'
    return this.wire.slots.map((s) => `${s.name} → ${s.serving || '—'}`).join(' · ')
  }
}
