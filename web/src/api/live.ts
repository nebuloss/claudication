/**
 * The topics the gateway announces changes of (internal/live), in its order:
 * what a panel names to be read again when its data changes
 * (docs/data-architecture.md). internal/live's TestTopicsMirrored holds this
 * list to the gateway's.
 */
export const TOPICS = [
  'accounts',
  'quota',
  'pool',
  'models',
  'clients',
  'running',
  'requests',
  'chats',
  'settings',
  'uptime',
] as const

export type Topic = (typeof TOPICS)[number]

type Listener = () => void

/** How long a change to the topics wanted waits for others before the stream is opened again. */
const SETTLE_MS = 50

/** How long after the browser gave up on a stream it is opened again. */
const RETRY_MS = 5_000

/**
 * How long notices are gathered before panels are told: one write can change
 * two topics — a setting, and the models it governs — and a panel showing
 * both should read once, not once per topic.
 */
const GATHER_MS = 30

/**
 * The tab's one stream of notices (GET /admin/events): open on the topics
 * the panels mounted now name, and telling each panel when one of its topics
 * changed. The stream carries no data; a panel told reads again.
 *
 * Opened again whenever the set of topics changes — a screen changed — and
 * closed while the tab is hidden: a hidden tab is told nothing, and reads
 * every panel again once when it is shown, which is the one notice it needs.
 * A stream that dropped and came back reads every panel again too, since what
 * changed while it was down was told to nobody.
 */
export class LiveStream {
  private source: EventSource | null = null
  private opened = ''
  private readonly wanted = new Map<Topic, Set<Listener>>()
  private readonly watchers = new Set<Listener>()
  private settle = 0
  private retry = 0
  private gather = 0
  private readonly due = new Set<Listener>()
  private connected = false
  // Whether the stream dropped since it was last open: what changed while it
  // was down was told to nobody, so its reopening reads everything.
  private dropped = false
  private visible = typeof document === 'undefined' || document.visibilityState === 'visible'

  constructor(private readonly url = '/admin/events') {
    if (typeof document !== 'undefined') {
      document.addEventListener('visibilitychange', () => {
        const visible = document.visibilityState === 'visible'
        if (visible === this.visible) return
        this.visible = visible
        if (visible) this.fireAll()
        this.schedule()
      })
    }
  }

  /** Has `fn` called when any of `topics` changes; returns what stops it. */
  on(topics: readonly Topic[], fn: Listener): () => void {
    for (const t of topics) {
      let set = this.wanted.get(t)
      if (set === undefined) this.wanted.set(t, (set = new Set()))
      set.add(fn)
    }
    this.schedule()
    return () => {
      for (const t of topics) {
        const set = this.wanted.get(t)
        set?.delete(fn)
        if (set?.size === 0) this.wanted.delete(t)
      }
      this.schedule()
    }
  }

  /** Whether notices are arriving: false while the stream is down, and while nothing asks for any. */
  get live(): boolean {
    return this.connected
  }

  /** Has `fn` called whenever `live` changes; returns what stops it. */
  watch(fn: Listener): () => void {
    this.watchers.add(fn)
    return () => this.watchers.delete(fn)
  }

  private schedule() {
    window.clearTimeout(this.settle)
    this.settle = window.setTimeout(() => this.open(), SETTLE_MS)
  }

  private topics(): string {
    return TOPICS.filter((t) => this.wanted.has(t)).join(',')
  }

  private open() {
    const topics = this.visible ? this.topics() : ''
    if (topics === this.opened && this.source !== null) return
    this.close()
    if (topics === '') return
    this.opened = topics
    const source = new EventSource(`${this.url}?topics=${topics}`)
    this.source = source
    source.onopen = () => {
      if (this.dropped) this.fireAll()
      this.dropped = false
      this.setConnected(true)
    }
    source.onerror = () => {
      this.dropped = true
      this.setConnected(false)
      // The browser retries a dropped stream itself, but not one refused —
      // a 401, or a gateway between workers answering 502. The panels read
      // at once: a lapsed session is the sign-in screen now, not at the next
      // floor read, and anything else keeps what they show.
      if (source.readyState === EventSource.CLOSED && this.source === source) {
        this.fireAll()
        window.clearTimeout(this.retry)
        this.retry = window.setTimeout(() => {
          if (this.source === source) {
            this.source = null
            this.open()
          }
        }, RETRY_MS)
      }
    }
    for (const t of topics.split(',') as Topic[]) {
      source.addEventListener(t, () => this.fire(t))
    }
    source.addEventListener('resync', () => this.fireAll())
  }

  private close() {
    window.clearTimeout(this.retry)
    this.source?.close()
    this.source = null
    this.opened = ''
    this.setConnected(false)
  }

  private setConnected(on: boolean) {
    if (on === this.connected) return
    this.connected = on
    for (const fn of this.watchers) fn()
  }

  private fire(t: Topic) {
    for (const fn of this.wanted.get(t) ?? []) this.due.add(fn)
    this.tell()
  }

  private fireAll() {
    for (const set of this.wanted.values()) for (const fn of set) this.due.add(fn)
    this.tell()
  }

  /** Tells each panel due once, whatever number of its topics changed. */
  private tell() {
    if (this.gather !== 0) return
    this.gather = window.setTimeout(() => {
      this.gather = 0
      const due = [...this.due]
      this.due.clear()
      for (const fn of due) fn()
    }, GATHER_MS)
  }
}

/** The tab's stream. */
export const liveStream = new LiveStream()

/**
 * What each admin read is computed from: the topics a panel showing it reads
 * again on. The same lists the gateway tags its cached reads with where the
 * routes are registered (internal/httpapi/admin, Routes); a panel that names
 * fewer goes stale on the one left out.
 */
export const READS = {
  accounts: ['accounts', 'quota', 'pool', 'requests'],
  accountModels: ['accounts', 'models'],
  overview: ['accounts', 'quota', 'pool', 'clients', 'requests'],
  clients: ['clients', 'accounts', 'models', 'requests'],
  clientLimits: ['clients', 'running', 'requests'],
  keyModels: ['clients', 'models'],
  families: ['models', 'settings'],
  config: ['settings'],
  usage: ['requests'],
  slots: ['clients', 'accounts', 'requests'],
  gatewayLive: ['running', 'requests'],
  requests: ['requests', 'chats'],
  running: ['running'],
  chats: ['chats', 'requests'],
  notices: ['accounts', 'quota', 'pool', 'models', 'clients', 'requests', 'settings'],
} as const satisfies Record<string, readonly Topic[]>
