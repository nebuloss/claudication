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

/**
 * What a slot can be bound to, grouped: the moving targets first — which
 * move with each release, each saying what it serves now — then each
 * provider's models. Only the server's choices, which are only what is
 * available to the client: nothing here is filtered or guessed.
 *
 * A slot bound to something no longer available keeps it, and the menu shows
 * it first, marked and not choosable again, so the operator sees what it is
 * and can only replace it with something that works.
 */
export function bindingOptions(
  choices: readonly BindingChoice[],
  current?: Pick<SlotWire, 'model' | 'bound' | 'title' | 'unavailable'>,
): BindingOption[] {
  const out: BindingOption[] = choices.map((c) =>
    c.model !== undefined && c.model !== ''
      ? { value: Binding.model(c.model).value, label: c.title || c.model, hint: c.model, group: providerTitle(c.provider) }
      : {
          value: Binding.follow(c.follows ?? '').value,
          label: c.title,
          hint: `now ${c.serving}`,
          group: 'Follow the newest',
        },
  )
  if (current === undefined) return out
  const bound = Binding.of(current)
  if (out.some((o) => o.value === bound.value)) return out
  return [
    {
      value: bound.value,
      label: current.title || bound.model || bound.follows,
      hint: current.unavailable !== undefined && current.unavailable !== '' ? `unavailable: ${current.unavailable}` : 'unavailable',
      group: 'Bound now',
      disabled: true,
    },
    ...out,
  ]
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
