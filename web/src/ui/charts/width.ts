import { useLayoutEffect, useRef, useState } from 'react'

/**
 * The width a chart is drawn at, in CSS pixels, so its coordinate system is
 * the screen's.
 *
 * The charts used to draw in a fixed 720-wide box and let the SVG scale, which
 * needs no measuring but scales the text with the plot: at a phone's 340px of
 * card the axis figures came out at five pixels, and a 240-high chart at a
 * hundred. Measured, text is set in real pixels at any width and the height is
 * the height asked for. `fallback` is drawn until the first measurement, and
 * where there is no layout to measure.
 */
export function useChartWidth<E extends Element>(fallback: number) {
  const ref = useRef<E>(null)
  const [width, setWidth] = useState(fallback)
  useLayoutEffect(() => {
    const el = ref.current
    if (el === null) return
    const read = () => {
      const w = Math.round(el.getBoundingClientRect().width)
      if (w > 0) setWidth(w)
    }
    read()
    if (typeof ResizeObserver === 'undefined') return
    // The element is sized by its container (w-full), never by the viewBox
    // this sets, so observing it cannot feed back into itself.
    const observer = new ResizeObserver(read)
    observer.observe(el)
    return () => observer.disconnect()
  }, [])
  return [ref, width] as const
}

/** Axis figures and labels, in pixels: legible at arm's length on a phone. */
export const AXIS_TEXT = 12
