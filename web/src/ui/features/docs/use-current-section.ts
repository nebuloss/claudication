import { useEffect, useState } from 'react'
import { type Section } from './model'

/**
 * Where the line between "read" and "not yet" sits, in pixels from the top.
 *
 * A little below the sticky header rather than at the very top of the
 * viewport: a heading is the thing you are reading from the moment it clears
 * the header, not from the moment it touches the top of the window. It also
 * matches the scroll-mt on the sections, so following a link from the contents
 * lands with that section marked rather than the one above it.
 */
const CURRENT_LINE = 120

/**
 * Which of the given sections is on screen.
 *
 * Its own module because it is the one piece of this page that is behaviour
 * rather than content or markup, and because what it does is worth stating
 * once where it can be read: position rather than IntersectionObserver. The
 * question is not which sections are visible — three usually are — but which
 * one is being read, and the answer is the last heading above the line. One
 * comparison per section against a number is easier to be right about than a
 * set of intersection ratios.
 *
 * Takes the sections rather than importing them, so it is a hook about a list
 * of ids and not about this page.
 */
export function useCurrentSection(sections: Section[]): string {
  const [current, setCurrent] = useState(sections[0]?.id ?? '')

  // The ids, joined, so the effect re-runs when the list actually changes
  // rather than whenever the caller happens to build a new array.
  const key = sections.map((s) => s.id).join(',')

  useEffect(() => {
    const ids = key === '' ? [] : key.split(',')

    const pick = () => {
      const present = ids
        .map((id) => document.getElementById(id))
        .filter((el): el is HTMLElement => el !== null)
      if (present.length === 0) return

      let active = present[0].id
      for (const el of present) {
        if (el.getBoundingClientRect().top > CURRENT_LINE) break
        active = el.id
      }
      // The last section is usually too short to reach the line: the page
      // stops scrolling before its heading gets there, so without this the
      // list would never mark the section you end on.
      const atBottom =
        window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 2
      setCurrent(atBottom ? present[present.length - 1].id : active)
    }

    // Throttled to one read per frame. A scroll handler that measures on every
    // event measures far more often than the screen is redrawn, and each
    // measurement forces layout.
    let frame = 0
    const onScroll = () => {
      if (frame !== 0) return
      frame = requestAnimationFrame(() => {
        frame = 0
        pick()
      })
    }

    pick()
    window.addEventListener('scroll', onScroll, { passive: true })
    window.addEventListener('resize', onScroll)
    return () => {
      if (frame !== 0) cancelAnimationFrame(frame)
      window.removeEventListener('scroll', onScroll)
      window.removeEventListener('resize', onScroll)
    }
  }, [key])

  return current
}
