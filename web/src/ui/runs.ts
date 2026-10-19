/**
 * How a panel's requests take turns: one in flight, and the newest question
 * wins.
 *
 * It used to be a boolean lock, which got the second half backwards. A request
 * started while another was in flight was dropped, so when the inputs changed
 * mid-poll — a different window on Usage, a new filter on the request log —
 * the request for the new inputs was never sent, and the panel went on showing
 * the old inputs' answer under the new inputs' controls until the next poll,
 * or for good with Live off. Nothing stopped a late answer landing over a newer
 * one either.
 *
 * So every run carries its own AbortController, and only the latest run may
 * commit what it fetched. How a new run treats one already in flight depends
 * on why it was started:
 *
 *   - `replace`: the inputs changed, or someone pressed Refresh. It is asking
 *     a different or a more urgent question than whatever is in flight, so it
 *     aborts that and goes ahead.
 *   - `append`: Load more. It pre-empts a poll, but not a first page being
 *     fetched: the cursor it would page on is about to be replaced.
 *   - `background`: a poll. It never jumps the queue, because whatever is in
 *     flight is already fetching what it would.
 */
export type RunMode = 'replace' | 'append' | 'background'

/** One request, from start to the moment its answer is used or thrown away. */
export class Run {
  private readonly controller = new AbortController()

  constructor(readonly mode: RunMode) {}

  /** Passed to the request, so a run that lost its turn stops downloading. */
  get signal(): AbortSignal {
    return this.controller.signal
  }

  abort(): void {
    this.controller.abort()
  }
}

export class Runs {
  private latest: Run | null = null

  /** A run to start, or null when this one should not be sent at all. */
  begin(mode: RunMode): Run | null {
    const running = this.latest
    if (running !== null) {
      if (mode === 'background') return null
      if (mode === 'append' && running.mode !== 'background') return null
      running.abort()
    }
    const run = new Run(mode)
    this.latest = run
    return run
  }

  /** Whether `run` may still commit: nothing has started since, and nothing cancelled it. */
  owns(run: Run): boolean {
    return this.latest === run
  }

  /** `run` has settled. Frees the turn if it still held it, and says whether it did. */
  end(run: Run): boolean {
    if (this.latest !== run) return false
    this.latest = null
    return true
  }

  /** The panel is going away: nothing in flight may land. */
  cancel(): void {
    this.latest?.abort()
    this.latest = null
  }
}
