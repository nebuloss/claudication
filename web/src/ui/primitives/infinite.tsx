import { useEffect, useRef } from 'react'

/**
 * The end of a list that pages from the server — the request log — calling
 * onReach while it is within `margin` of the viewport. `version` is
 * what changes when a reach was answered — the count loaded — so a sentinel
 * still in view after a page arrived asks again, rather than waiting for a
 * scroll that may never come. Clipped by any scrolling box it is in, as the
 * viewport sees it, so one inside a capped table fires at that table's end.
 */
export function Sentinel({
  onReach,
  version,
  disabled = false,
  margin = '400px',
  row = false,
}: {
  onReach: () => void
  version: number
  disabled?: boolean
  margin?: string
  /** A table row, for the end of a table's body. */
  row?: boolean
}) {
  const ref = useRef<HTMLElement | null>(null)
  const reach = useRef(onReach)
  reach.current = onReach
  useEffect(() => {
    const el = ref.current
    if (disabled || el === null || typeof IntersectionObserver === 'undefined') return
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) reach.current()
    }, { rootMargin: margin })
    io.observe(el)
    return () => io.disconnect()
  }, [disabled, margin, version])
  if (row) {
    return (
      <tr ref={(el) => void (ref.current = el)} aria-hidden>
        <td colSpan={99} className="h-px p-0" />
      </tr>
    )
  }
  return <div ref={(el) => void (ref.current = el)} aria-hidden className="h-px" />
}
