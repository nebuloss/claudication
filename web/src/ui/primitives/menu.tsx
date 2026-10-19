import { useEffect, useRef, type ReactNode } from 'react'
import { useEscapeKey } from '../hooks'
import { Popover, menuItem, useContainedScroll, useDismiss } from './popover'

/** One line of a menu that does something when clicked. */
export function MenuRow({
  onClick,
  active = false,
  children,
}: {
  onClick: () => void
  active?: boolean
  children: ReactNode
}) {
  return (
    <button
      type="button"
      role="menuitem"
      onClick={onClick}
      className={`${menuItem()} ${active ? 'text-primary' : ''}`}
    >
      {children}
    </button>
  )
}

/**
 * A menu opened where the pointer is, on right-click.
 *
 * It has no anchor to follow, so a scroll of the page closes it — the point it
 * was opened at is somewhere else once the page moves. A scroll inside the
 * menu does not. The first item takes focus, so the keyboard's own
 * context-menu key leads somewhere usable.
 */
export function ContextMenu({
  at,
  label,
  onClose,
  children,
}: {
  at: { x: number; y: number }
  label: string
  onClose: () => void
  children: ReactNode
}) {
  const panel = useRef<HTMLDivElement>(null)
  useEscapeKey(onClose)
  useDismiss(true, [panel], onClose)
  useContainedScroll(panel, true)

  useEffect(() => {
    const onScroll = (e: Event) => {
      if (panel.current?.contains(e.target as Node) === true) return
      onClose()
    }
    window.addEventListener('scroll', onScroll, true)
    window.addEventListener('resize', onClose)
    return () => {
      window.removeEventListener('scroll', onScroll, true)
      window.removeEventListener('resize', onClose)
    }
  }, [onClose])

  useEffect(() => {
    panel.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus()
  }, [])

  // Kept on screen: a right-click near an edge would otherwise open the menu
  // past it.
  const width = 232
  const left = Math.max(8, Math.min(at.x, window.innerWidth - width - 8))
  const top = Math.max(8, Math.min(at.y, window.innerHeight - 160))

  return (
    <Popover
      panelRef={panel}
      at={{ left, top, width, maxHeight: window.innerHeight - top - 8 }}
      role="menu"
      label={label}
    >
      {children}
    </Popover>
  )
}
