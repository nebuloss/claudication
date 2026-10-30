import type { ReactNode } from 'react'
import { ICON } from '../primitives/icons'
import { Tip } from '../primitives/info'
import { Popover, useAnchoredPanel } from '../primitives/popover'
import { ACCENTS, ACCENT_LABEL, accentColor, type Accent } from './accents'
import { BRANDS, type BrandName } from './marks'

/**
 * Whose a row is, at a glance: the provider an account signs in to, the tool
 * a client is. Each is drawn as its vendor draws it (marks.ts); a client type
 * with no published mark — crush, a custom client — gets the generic
 * terminal, so every row still leads with something.
 */

/**
 * The product an account signs in to, by provider id: what its rows and its
 * page wear. ChatGPT has no mark of its own in the set, so it is OpenAI's on
 * ChatGPT's green (Mark, 'chatgpt'), which also keeps it apart from the
 * plain OpenAI mark heading its accounts.
 */
const PROVIDER: Record<string, BrandName | 'chatgpt'> = {
  anthropic: 'claude',
  openai: 'chatgpt',
  google: 'gemini',
}

/** A provider's company, by provider id: what heads its accounts, above rows wearing the product's. */
const COMPANY: Record<string, BrandName> = {
  anthropic: 'anthropic',
  openai: 'openai',
  google: 'google',
}

/** ChatGPT's green, behind OpenAI's mark. */
const CHATGPT_GREEN = '#10a37f'

/** A client type's mark, by type id (internal/service/clients). */
const CLIENT: Record<string, BrandName> = {
  'claude-code': 'claudecode',
  codex: 'codex',
  opencode: 'opencode',
  'antigravity-cli': 'antigravity',
}

const SIZE = { sm: 'size-4', md: 'size-5', lg: 'size-7' } as const

function Mark({ name, size }: { name: BrandName | 'chatgpt' | undefined; size: keyof typeof SIZE }) {
  if (name === 'chatgpt') {
    const m = BRANDS.openai
    return (
      <svg viewBox="0 0 24 24" aria-hidden className={`${SIZE[size]} shrink-0`}>
        <circle cx="12" cy="12" r="12" fill={CHATGPT_GREEN} />
        <g
          transform="translate(4.5 4.5) scale(0.625)"
          fill="#fff"
          fillRule="evenodd"
          dangerouslySetInnerHTML={{ __html: m.body }}
        />
      </svg>
    )
  }
  if (name === undefined) {
    return (
      <svg viewBox="0 0 24 24" aria-hidden className={`${SIZE[size]} shrink-0 fill-current text-on-surface-variant`}>
        <path d={ICON.clients} />
      </svg>
    )
  }
  const m: { viewBox: string; fill?: string; fillRule?: string; body: string } = BRANDS[name]
  return (
    <svg
      viewBox={m.viewBox}
      aria-hidden
      fill={m.fill}
      fillRule={m.fillRule as 'evenodd' | 'nonzero' | undefined}
      className={`${SIZE[size]} shrink-0`}
      // The vendor's own markup, generated from the icon package's files
      // (marks.ts): paths, gradients and filters, nothing that runs.
      dangerouslySetInnerHTML={{ __html: m.body }}
    />
  )
}

/** The mark of the product an account signs in to: Claude, ChatGPT, Gemini. */
export function ProviderMark({ provider, size = 'md' }: { provider: string; size?: keyof typeof SIZE }) {
  return <Mark name={PROVIDER[provider]} size={size} />
}

/** The mark of a provider's company — Anthropic, OpenAI, Google — over the accounts it groups. */
export function CompanyMark({ provider, size = 'sm' }: { provider: string; size?: keyof typeof SIZE }) {
  return <Mark name={COMPANY[provider]} size={size} />
}

/**
 * A brand mark on the round tile a list row leads with, in the item's accent
 * (accents.ts): the same size and place whatever the mark, so a column of
 * rows lines up, and two of one provider or one type told apart by colour.
 */
export function MarkTile({
  accent,
  size = 'md',
  children,
}: {
  accent?: string
  size?: 'sm' | 'md' | 'lg'
  children: ReactNode
}) {
  return (
    <span
      className={`grid shrink-0 place-items-center rounded-full ${TILE[size]}`}
      style={{ background: accentColor(accent) }}
    >
      {children}
    </span>
  )
}

/** A tile's size, and the mark's in it. */
const TILE = { sm: 'size-6 [&>svg]:size-3.5', md: 'size-8', lg: 'size-11 [&>svg]:size-6' } as const

/**
 * An item's mark on its accent's tile, which opens the palette on a click: the
 * page title's, where the colour is changed. Each swatch is the mark on that
 * colour, so the choice is seen as it will look.
 */
export function AccentPicker({
  accent,
  label,
  onPick,
  children,
}: {
  accent?: string
  /** What is being coloured, for the button's name: "this account". */
  label: string
  onPick: (accent: Accent) => void
  /** The mark, as the tile shows it. */
  children: ReactNode
}) {
  const p = useAnchoredPanel({ width: 232 })
  return (
    <>
      <Tip tip={p.open ? undefined : `Change the colour of ${label}`}>
        <button
          ref={p.anchor}
          type="button"
          aria-haspopup="dialog"
          aria-expanded={p.open}
          aria-label={`Colour of ${label}`}
          onClick={p.toggle}
          className="state-layer rounded-full ring-offset-2 ring-offset-surface hover:ring-2 hover:ring-outline-variant"
        >
          <MarkTile accent={accent} size="lg">
            {children}
          </MarkTile>
        </button>
      </Tip>
      {p.at !== null && (
        <Popover panelRef={p.panel} at={p.at} role="dialog" label={`Colour of ${label}`} className="p-3">
          <div className="mb-2 px-1 text-xs font-medium text-on-surface-variant">Colour</div>
          <div className="grid grid-cols-4 gap-2" role="radiogroup" aria-label="Colour">
            {ACCENTS.map((a) => {
              const on = a === accent
              return (
                <Tip key={a} tip={ACCENT_LABEL[a]}>
                  <button
                    type="button"
                    role="radio"
                    aria-checked={on}
                    aria-label={ACCENT_LABEL[a]}
                    onClick={() => {
                      p.close()
                      if (!on) onPick(a)
                    }}
                    className={`state-layer grid place-items-center rounded-full p-0.5 ${
                      on ? 'ring-2 ring-primary' : 'ring-1 ring-transparent hover:ring-outline-variant'
                    }`}
                  >
                    <MarkTile accent={a} size="lg">
                      {children}
                    </MarkTile>
                  </button>
                </Tip>
              )
            })}
          </div>
        </Popover>
      )}
    </>
  )
}

/** The mark of the tool a client is, by its type; the generic terminal for a custom one. */
export function ClientMark({ type, size = 'md' }: { type: string; size?: keyof typeof SIZE }) {
  return <Mark name={CLIENT[type]} size={size} />
}
