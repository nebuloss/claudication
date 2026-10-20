/**
 * The kinds of client the gateway configures, and a client as one of them.
 *
 * The table itself is the server's (internal/service/clients, served at
 * /admin/client-types): every type's slots, labels, default names and
 * targets, which APIs it can be pointed at and why. These classes only read
 * it, so a slot's label here and in the store cannot disagree. Pure: no
 * React, so `make test-web` exercises it.
 */

import type {
  ClientTypesWire,
  ClientWire,
  DialectId,
  FollowTarget,
  Model,
  SlotWire,
  TypeSpec,
} from '../../api/types'

/** What each provider is called on a screen. */
export const PROVIDER_TITLES: Record<string, string> = { anthropic: 'Claude', openai: 'ChatGPT' }

/** A provider's name, or the id itself for one this UI does not know. */
export function providerTitle(id: string | undefined): string {
  return PROVIDER_TITLES[id ?? 'anthropic'] ?? id ?? ''
}

/** Which providers an API reaches: Messages only Claude, Responses both. */
export function reaches(dialect: DialectId, provider: string | undefined): boolean {
  return dialect === 'openai' || (provider ?? 'anthropic') === 'anthropic'
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

  get slots(): TypeSpec['slots'] {
    return this.spec.slots
  }

  get why(): string {
    return this.spec.why ?? ''
  }

  get namesItsChats(): boolean {
    return this.spec.names_its_chats
  }

  get dialects(): DialectId[] {
    return this.spec.dialects
  }

  get defaultDialect(): DialectId {
    return this.spec.dialects[0] ?? 'anthropic'
  }

  /** Whether there is a choice of API to offer. */
  get choosesDialect(): boolean {
    return this.spec.dialects.length > 1
  }

  speaks(d: DialectId): boolean {
    return this.spec.dialects.includes(d)
  }

  /** What every slot name must contain on a dialect, or ''. */
  mark(d: DialectId): string {
    return this.spec.marks?.[d] ?? ''
  }

  /** Whether a name is one the client handles right on a dialect. */
  fits(d: DialectId, name: string): boolean {
    const m = this.mark(d)
    return m === '' || name.includes(m)
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

  /** The targets a slot can follow now. */
  get follows(): FollowTarget[] {
    return this.wire.follows
  }
}

const FALLBACK: TypeSpec = {
  id: 'custom', label: 'Custom', dialects: ['anthropic', 'openai'], slots: [], custom: true, names_its_chats: false,
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
}

/**
 * Everything a slot can be bound to, grouped: the moving targets first —
 * which move with each release — then each provider's models. A model the
 * client's API cannot reach is listed all the same, marked; the slot then
 * shows the warning.
 */
export function bindingOptions(
  models: readonly Model[],
  follows: readonly FollowTarget[],
  dialect: DialectId,
): BindingOption[] {
  const out: BindingOption[] = follows.map((f) => ({
    value: Binding.follow(f.id).value,
    label: f.title,
    hint: f.serving ?? f.problem ?? '',
    group: 'Follow the newest',
  }))
  const real = models.filter((m) => m.virtual !== true)
  for (const provider of ['anthropic', 'openai']) {
    for (const m of real.filter((x) => (x.provider ?? 'anthropic') === provider)) {
      out.push({
        value: Binding.model(m.id).value,
        label: m.display_name ?? m.id,
        hint: reaches(dialect, provider) ? m.id : `${m.id} — not on this client's API`,
        group: providerTitle(provider),
      })
    }
  }
  return out
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

  get dialect(): DialectId {
    return this.wire.dialect
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

  /** The slots its API cannot be served on as bound, for the warnings. */
  get unreachable(): SlotWire[] {
    return this.wire.slots.filter((s) => s.unreachable === true)
  }

  /** The slots that cannot be served at all now. */
  get failing(): SlotWire[] {
    return this.wire.slots.filter((s) => (s.problem ?? '') !== '')
  }

  /** One line for a list row: each slot's name and what it serves. */
  get summary(): string {
    if (!this.namesSlots) return 'real model ids'
    if (this.wire.slots.length === 0) return 'no model names yet'
    return this.wire.slots.map((s) => `${s.name} → ${s.serving || '—'}`).join(' · ')
  }
}
