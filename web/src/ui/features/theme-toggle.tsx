import { useTheme, type ThemeMode } from '../theme'
import { Segmented } from '../primitives'

const ICONS: Record<ThemeMode, string> = {
  light:
    'M12 7a5 5 0 100 10 5 5 0 000-10zm0-5v3m0 14v3M2 12h3m14 0h3M4.2 4.2l2.1 2.1m11.4 11.4l2.1 2.1M4.2 19.8l2.1-2.1M17.7 6.3l2.1-2.1',
  auto: 'M4 5h16a1 1 0 011 1v9a1 1 0 01-1 1H4a1 1 0 01-1-1V6a1 1 0 011-1zM8 20h8',
  dark: 'M20 13.5A8 8 0 1110.5 4a6.5 6.5 0 009.5 9.5z',
}

const OPTIONS: { id: ThemeMode; label: string }[] = [
  { id: 'light', label: 'Light' },
  { id: 'auto', label: 'Follow the system' },
  { id: 'dark', label: 'Dark' },
]

/**
 * Three states rather than a switch: "auto" is a standing instruction to keep
 * following the machine, which a two-way toggle cannot express — flipping a
 * switch to match the OS today silently stops tracking it tomorrow.
 */
export default function ThemeToggle() {
  const { mode, setMode } = useTheme()

  return (
    <Segmented
      label="Colour theme"
      value={mode}
      onChange={setMode}
      options={OPTIONS.map((o) => ({
        id: o.id,
        label: o.label,
        content: (
          <svg
            viewBox="0 0 24 24"
            className="size-4"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.8"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden
          >
            <path d={ICONS[o.id]} />
          </svg>
        ),
      }))}
    />
  )
}
