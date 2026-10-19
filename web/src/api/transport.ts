/* How the UI talks to the admin API: one transport, and the errors it raises. */

/** An error carrying the status and machine-readable type from the server. */
export class ApiError extends Error {
  readonly status: number
  readonly type: string

  constructor(status: number, type: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.type = type
  }

  /** True when the session is missing or expired and the user must sign in. */
  get isUnauthenticated(): boolean {
    return this.status === 401
  }
}

/**
 * Where a lapsed session is reported: to one handler, which the shell installs,
 * because only the shell can put the sign-in screen up.
 *
 * The transport reports every 401 here as it raises it, so a write nobody
 * wrapped — a button's bare fetch, a fire-and-forget refresh — signs out like
 * any other instead of failing silently. Two endpoints are the exception:
 * changing the password and deleting the admin account both ask for it again,
 * and their 401 means it was mistyped, which must not sign anyone out over a
 * typo; and the calls that sign in, or ask whether anyone is, expect one.
 * Those say so when they are called (`answersAuth`).
 *
 * useLoader and useAction still ask, to know whether to show an error: a
 * failure already handed to the shell is not one to print. So a report is
 * once per error, however many layers pass it on.
 */
export class SessionExpiry {
  private handler: (() => void) | null = null
  private readonly handled = new WeakSet<ApiError>()

  /** Installs the handler, and returns what takes it away again. */
  listen(handler: () => void): () => void {
    this.handler = handler
    return () => {
      if (this.handler === handler) this.handler = null
    }
  }

  /**
   * Hands `err` to the shell when it says the session lapsed. False when it is
   * any other failure, or when nobody is signed in to be signed out — on the
   * sign-in screen a 401 is the expected answer, not news.
   */
  report(err: unknown): boolean {
    if (this.handler === null || !(err instanceof ApiError) || !err.isUnauthenticated) return false
    if (!this.handled.has(err)) {
      this.handled.add(err)
      this.handler()
    }
    return true
  }
}

export const sessionExpiry = new SessionExpiry()

/** Anything thrown at us, as a sentence worth showing. */
export function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

/**
 * A path with every interpolated value encoded as one segment or one query
 * value: path`/admin/keys/${id}/models`. The twenty encodeURIComponent calls
 * this replaced were each one forgotten call away from an id with a slash in
 * it addressing a different resource.
 */
export function path(strings: TemplateStringsArray, ...values: (string | number)[]): string {
  return strings.reduce(
    (out, s, i) => out + s + (i < values.length ? encodeURIComponent(String(values[i])) : ''),
    '',
  )
}

/** How a call wants its answer and its failures treated. */
export type CallOptions = {
  body?: unknown
  /** The caller's way of saying the answer is no longer wanted. */
  signal?: AbortSignal
  /**
   * This call is itself about signing in — it checks a password, or asks
   * whether there is a session — so its 401 is its answer, not news of a
   * session that lapsed. Reporting it would sign the operator out over a
   * mistyped password, and would have the shell's own "am I signed in?"
   * probe report itself and probe again.
   */
  answersAuth?: boolean
}

/**
 * The one way the UI reaches the admin API: JSON in and out, a timeout, the
 * caller's cancellation, and the error envelope turned into an ApiError.
 */
export class HttpTransport {
  constructor(
    private readonly expiry: SessionExpiry,
    /**
     * How long a call may hang before it is reported as failed.
     *
     * fetch has no timeout of its own, so a gateway that accepts a connection
     * and then stops answering — mid-restart, or reachable over a tunnel that
     * has gone away — leaves every panel spinning for ever with nothing to
     * click. Generous, because a few of these do real work: probing an
     * account talks to Anthropic.
     */
    private readonly timeoutMs = 30_000,
  ) {}

  get<T>(url: string, signal?: AbortSignal, options: CallOptions = {}): Promise<T> {
    return this.call<T>('GET', url, { ...options, signal })
  }

  post<T>(url: string, body?: unknown, options: CallOptions = {}): Promise<T> {
    return this.call<T>('POST', url, { ...options, body })
  }

  put<T>(url: string, body?: unknown): Promise<T> {
    return this.call<T>('PUT', url, { body })
  }

  patch<T>(url: string, body?: unknown): Promise<T> {
    return this.call<T>('PATCH', url, { body })
  }

  /** For an endpoint whose answer carries nothing the caller needs. */
  async send(method: 'POST' | 'PUT' | 'DELETE', url: string, options: CallOptions = {}): Promise<void> {
    await this.call<unknown>(method, url, options)
  }

  /**
   * One call. `signal` is folded into the timeout's own controller, and an
   * abort it asked for is rethrown as the AbortError it is rather than
   * reported as the gateway failing to answer.
   */
  async call<T>(method: string, url: string, { body, signal, answersAuth = false }: CallOptions = {}): Promise<T> {
    let response: Response
    const abort = new AbortController()
    const timer = window.setTimeout(() => abort.abort(), this.timeoutMs)
    const cancel = () => abort.abort()
    if (signal?.aborted) abort.abort()
    signal?.addEventListener('abort', cancel, { once: true })
    try {
      response = await fetch(url, {
        method,
        headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
        body: body === undefined ? null : JSON.stringify(body),
        credentials: 'same-origin',
        signal: abort.signal,
      })
    } catch (cause) {
      if (signal?.aborted) throw new DOMException('The request was cancelled.', 'AbortError')
      if (abort.signal.aborted) {
        throw new ApiError(0, 'timeout', `The gateway did not answer within ${this.timeoutMs / 1000} seconds.`)
      }
      throw new ApiError(0, 'network_error', `Could not reach the gateway: ${String(cause)}`)
    } finally {
      window.clearTimeout(timer)
      signal?.removeEventListener('abort', cancel)
    }

    const text = await response.text()
    let payload: unknown = null
    if (text.length > 0) {
      try {
        payload = JSON.parse(text)
      } catch {
        // A non-JSON body is reported verbatim below.
      }
    }

    if (!response.ok) {
      const envelope = payload as { error?: { message?: string; type?: string } } | null
      // `||`, not `??`: text.trim() is always a string, so `??` never reaches
      // the status fallback and an empty error body renders as an empty message —
      // which the panels then show as their "nothing here yet" empty state.
      const err = new ApiError(
        response.status,
        envelope?.error?.type ?? 'http_error',
        envelope?.error?.message || text.trim() || `HTTP ${response.status}`,
      )
      if (!answersAuth) this.expiry.report(err)
      throw err
    }
    return payload as T
  }
}
