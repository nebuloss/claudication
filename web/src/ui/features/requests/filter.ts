import { pathOf } from '../../route'

/**
 * What the request log is narrowed to, as an immutable value.
 *
 * Filters come off the URL so a link can carry them: /requests?chat=…&status=failed
 * is what the chat table links to, which is why that table needs no request
 * list of its own. A URL is also something an operator can edit, share and
 * bookmark, which a filter held only in component state never is.
 *
 * It was a plain record spread at every call site, and that cost a bug: the
 * parser and the menu that writes `status` disagreed about which values exist,
 * so "Successful only" was dropped on reload. Every reading and writing of the
 * query string is a method here now, and every change returns a new filter,
 * so a component holding one can compare it by its query.
 */

/**
 * The columns that hold a set of values rather than one.
 *
 * A menu is a column of checkboxes, so "sonnet or haiku" has to be sayable;
 * one tick is a one-element set and needs no special case. An empty string is
 * a real member — a refused request has no model, the gateway's own titling
 * rows carry no client — so `?model=` means the rows with nothing there.
 */
export const SET_FIELDS = [
  'chat',
  'key',
  'account',
  'model',
  'client',
  'ip',
  'code',
  'message',
  'kind',
] as const
export type SetField = (typeof SET_FIELDS)[number]

/**
 * A predicate over statuses rather than one of them, which is why it is not
 * in `code`: 429 and 500 are two values, "anything that went wrong" is a
 * question about codes plus stream errors. The two compose. Exactly the two
 * the Status menu writes; anything else in a URL reads as unfiltered rather
 * than as an error, because a URL is something people edit.
 */
export type Outcome = '' | 'ok' | 'failed'
const OUTCOMES: readonly Outcome[] = ['ok', 'failed']

/** The fields a filter is made of, for a caller naming only what it means. */
export type FilterFields = Record<SetField, readonly string[]> & { status: Outcome }

/** Whose requests: one account's, one key's, or every request when empty. */
export type RequestScope = { account?: string; key?: string }

function emptyFields(): FilterFields {
  return {
    chat: [],
    key: [],
    account: [],
    model: [],
    client: [],
    ip: [],
    code: [],
    message: [],
    kind: [],
    status: '',
  }
}

/** Where a scope's request log is: the item's own Requests page, or Activity. */
function logPath(scope: RequestScope): string {
  if (scope.account !== undefined) return pathOf('accounts', scope.account, 'requests')
  if (scope.key !== undefined) return pathOf('clients', scope.key, 'requests')
  return '/activity/requests'
}

export class RequestFilter {
  private constructor(private readonly fields: FilterFields) {}

  /** Nothing narrowed. */
  static readonly none = new RequestFilter(emptyFields())

  /**
   * A filter naming only what the caller means. Spelling out every empty
   * field at each call site is how a new field silently fails to compile
   * somewhere else — which is exactly what adding `client` did.
   */
  static of(narrow: Partial<FilterFields>): RequestFilter {
    return new RequestFilter({ ...emptyFields(), ...narrow })
  }

  /** The query string as a filter. Unknown parameters are ignored. */
  static fromSearch(search: string): RequestFilter {
    const q = new URLSearchParams(search)
    const f = emptyFields()
    // getAll, because a set travels as a repeated parameter: a model name is
    // not ours to reserve a comma in.
    for (const field of SET_FIELDS) f[field] = q.getAll(field)
    const status = q.get('status') ?? ''
    f.status = OUTCOMES.find((o) => o === status) ?? ''
    return new RequestFilter(f)
  }

  values(field: SetField): readonly string[] {
    return this.fields[field]
  }

  get status(): Outcome {
    return this.fields.status
  }

  /** Whether a column is narrowing the log, for its heading's funnel. */
  narrows(field: SetField): boolean {
    return this.fields[field].length > 0 || (field === 'code' && this.fields.status !== '')
  }

  /** The value ticked if it was not, unticked if it was. */
  toggled(field: SetField, value: string): RequestFilter {
    const now = this.fields[field]
    return this.with(field, now.includes(value) ? now.filter((v) => v !== value) : [...now, value])
  }

  /** Narrowed to this one value of a column: a click on a cell. */
  only(field: SetField, value: string): RequestFilter {
    return this.with(field, [value])
  }

  without(field: SetField, value: string): RequestFilter {
    return this.with(field, this.fields[field].filter((v) => v !== value))
  }

  cleared(field: SetField): RequestFilter {
    return this.with(field, [])
  }

  /** The outcome set, or lifted when it is already the one in force. */
  toggledStatus(status: Exclude<Outcome, ''>): RequestFilter {
    return new RequestFilter({ ...this.fields, status: this.fields.status === status ? '' : status })
  }

  withStatus(status: Outcome): RequestFilter {
    return new RequestFilter({ ...this.fields, status })
  }

  /** One entry per value filtered on: what the chips above the table show. */
  pinned(): { field: SetField; value: string }[] {
    return SET_FIELDS.flatMap((field) => this.fields[field].map((value) => ({ field, value })))
  }

  /** The filter as a query string, without the path in front of it. */
  query(): string {
    const q = new URLSearchParams()
    for (const field of SET_FIELDS) for (const one of this.fields[field]) q.append(field, one)
    if (this.fields.status !== '') q.set('status', this.fields.status)
    return q.toString()
  }

  /** As the admin API takes it: a set as an array, the outcome as a value. */
  params(): Record<string, string | readonly string[]> {
    return { ...this.fields }
  }

  /** With a scope's own field set: the account or key whose page this is. */
  scopedTo(scope: RequestScope): RequestFilter {
    let f: RequestFilter = this
    if (scope.account !== undefined) f = f.only('account', scope.account)
    if (scope.key !== undefined) f = f.only('key', scope.key)
    return f
  }

  /**
   * Without the field a scope sets. The scope is in the path, so it is always
   * applied and never written into the query string; the rest of the filter is.
   */
  outside(scope: RequestScope): RequestFilter {
    let f: RequestFilter = this
    if (scope.account !== undefined) f = f.cleared('account')
    if (scope.key !== undefined) f = f.cleared('key')
    return f
  }

  /** This filter on a scope's log page. */
  pathIn(scope: RequestScope): string {
    const s = this.query()
    const path = logPath(scope)
    return s === '' ? path : `${path}?${s}`
  }

  /**
   * A link to the log, on the page it belongs to: narrowed to one account or
   * one key and not the other, that item's own Requests page, where the rest
   * of its pages are a tab away; otherwise Activity's.
   */
  href(): string {
    const { account, key } = this.fields
    const [oneAccount] = account
    const [oneKey] = key
    if (oneAccount !== undefined && account.length === 1 && key.length === 0) {
      return this.cleared('account').pathIn({ account: oneAccount })
    }
    if (oneKey !== undefined && key.length === 1 && account.length === 0) {
      return this.cleared('key').pathIn({ key: oneKey })
    }
    return this.pathIn({})
  }

  private with(field: SetField, values: readonly string[]): RequestFilter {
    return new RequestFilter({ ...this.fields, [field]: values })
  }
}

/** A link to the request log, narrowed to whatever the caller names. */
export function requestsHref(narrow: Partial<FilterFields>): string {
  return RequestFilter.of(narrow).href()
}
