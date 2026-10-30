/**
 * The colours an account or a client can wear (store.Accents, in the order
 * the gateway hands them out): the tile its mark sits on, which tells two
 * accounts of one provider, or two clients of one type, apart. Each is a
 * --color-accent-<name> token in styles/index.css, light and dark.
 * internal/store's TestAccentsMirrored holds this list to the gateway's.
 */
export const ACCENTS = ['blue', 'violet', 'teal', 'pink', 'indigo', 'cyan', 'sand', 'slate'] as const

export type Accent = (typeof ACCENTS)[number]

/** An accent's colour as a CSS value; the neutral tile for none, or one this UI does not know. */
export function accentColor(accent: string | undefined): string {
  return (ACCENTS as readonly string[]).includes(accent ?? '')
    ? `var(--color-accent-${accent})`
    : 'var(--color-surface-high)'
}

/** What each accent is called in the picker. */
export const ACCENT_LABEL: Record<Accent, string> = {
  blue: 'Blue',
  violet: 'Violet',
  teal: 'Teal',
  pink: 'Pink',
  indigo: 'Indigo',
  cyan: 'Cyan',
  sand: 'Sand',
  slate: 'Slate',
}
