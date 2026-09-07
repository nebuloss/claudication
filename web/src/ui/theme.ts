import { useCallback, useEffect, useState } from 'react'

/**
 * Light / dark / auto, the same three states the other projects here use.
 *
 * An explicit choice stamps data-theme on the root; 'auto' stamps nothing and
 * lets prefers-color-scheme decide. The palette is defined light-first in CSS
 * with dark redefined in both a media query and a [data-theme] block, which is
 * what lets an explicit choice win in either direction — and what lets 'auto'
 * work with no JavaScript involved at all.
 */
export type ThemeMode = 'auto' | 'light' | 'dark'

const KEY = 'claudication.theme'
const DARK_QUERY = '(prefers-color-scheme: dark)'

function prefersDark(): boolean {
  return window.matchMedia?.(DARK_QUERY).matches ?? false
}

function read(): ThemeMode {
  try {
    const v = localStorage.getItem(KEY)
    if (v === 'light' || v === 'dark') return v
  } catch {
    // A private window, or a browser set to block site data. Not an error:
    // the theme simply stops being remembered.
  }
  return 'auto'
}

function write(mode: ThemeMode) {
  try {
    if (mode === 'auto') localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, mode)
  } catch {
    // Same as above — the choice still applies to this tab.
  }
}

function apply(mode: ThemeMode) {
  const root = document.documentElement
  if (mode === 'auto') delete root.dataset.theme
  else root.dataset.theme = mode

  // The <meta theme-color> pair cannot express an explicit override, so drive
  // the browser chrome from the palette itself after the change has landed.
  requestAnimationFrame(() => {
    const bg = getComputedStyle(root).getPropertyValue('--color-surface').trim()
    const meta = document.querySelector('meta[name="theme-color"]')
    if (meta !== null && bg !== '') meta.setAttribute('content', bg)
  })
}

/**
 * useTheme returns the current mode, what it resolves to, and a setter.
 *
 * While the mode is 'auto' it keeps listening, so changing the OS theme flips
 * the page without a reload — which is the whole point of choosing 'auto'
 * rather than picking a side.
 */
export function useTheme() {
  const [mode, setModeState] = useState<ThemeMode>(read)
  const [isDark, setIsDark] = useState<boolean>(() => {
    const m = read()
    return m === 'auto' ? prefersDark() : m === 'dark'
  })

  const setMode = useCallback((next: ThemeMode) => {
    write(next)
    setModeState(next)
    setIsDark(next === 'auto' ? prefersDark() : next === 'dark')
    apply(next)
  }, [])

  useEffect(() => {
    if (mode !== 'auto') return
    const mq = window.matchMedia(DARK_QUERY)
    const onChange = () => setIsDark(mq.matches)
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [mode])

  return { mode, isDark, setMode }
}
