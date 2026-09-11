import { type CSSProperties, type ReactNode } from 'react'

/**
 * How a mark is filled, and what that filling means.
 *
 * A Paint is one object answering the same question in three places — the SVG
 * shape, the HTML legend swatch, and whatever `<defs>` the fill needs — so a
 * legend can never disagree with the bar it describes. That disagreement is
 * the commonest chart bug there is, and it is impossible to have here.
 *
 * The split between Solid and Hatch is not decoration. Hue carries *identity*:
 * which model, which account, which kind of token. Texture and value carry
 * *state*: this part of that identity failed. Keeping them apart means a chart
 * can show both at once without the two vocabularies colliding — and a card
 * showing eight models does not have to surrender one of its eight hues to
 * mean "error".
 */
export interface Paint {
  /** Props to spread onto an SVG shape. */
  shape(): { fill: string; style?: CSSProperties }
  /** Style for an HTML legend swatch. */
  swatch(): CSSProperties
  /** What this paint contributes to `<defs>`, or null. */
  defs(): ReactNode
  /** The same identity, marked as a degraded state. */
  shaded(): Paint
}

/**
 * How far a shaded fill is darkened.
 *
 * Bounded at both ends. Too little and the state is invisible; too much and in
 * a dark theme it walks into the background, which is the direction black
 * lies in. Measured as CIE76 dE at 58%, for both colours these charts use:
 *
 *                      base/band  band/stripe  band/card
 *   light  primary          12.4         14.2       70.4
 *   dark   primary          18.3         22.0       57.0
 *   light  series-1         15.5         19.0       73.2
 *   dark   series-1         16.5         19.5       56.0
 *
 * The last column is the one that bounds it, and at 58% it stays well clear in
 * both themes.
 */
const SHADE = 58

function darker(colour: string): string {
  return `color-mix(in oklab, ${colour} ${SHADE}%, black)`
}

/** A flat fill. */
export class Solid implements Paint {
  constructor(readonly colour: string) {}

  shape() {
    return { fill: this.colour }
  }

  swatch(): CSSProperties {
    return { background: this.colour }
  }

  defs(): ReactNode {
    return null
  }

  shaded(): Paint {
    return new Hatch(this.colour)
  }
}

/**
 * A darker, diagonally striped tone of the same colour.
 *
 * Colour alone is a single point of failure in a chart: it is what a viewer
 * may not perceive, a projector washes out, and a two-pixel band gives too
 * little of to judge by. The stripes are a second channel that survives all
 * three.
 */
export class Hatch implements Paint {
  /** Derived from the colour, so two series sharing one need one pattern. */
  readonly id: string

  constructor(readonly colour: string) {
    this.id = 'hatch-' + colour.replace(/[^a-zA-Z0-9]+/g, '-').replace(/(^-|-$)/g, '')
  }

  shape() {
    return { fill: `url(#${this.id})` }
  }

  swatch(): CSSProperties {
    // The flat colour underneath, so a browser without color-mix still shows
    // the identity rather than nothing.
    return {
      background: this.colour,
      backgroundImage:
        `repeating-linear-gradient(45deg, ${darker(this.colour)} 0 2.5px, ` +
        `var(--color-surface-container) 2.5px 4px)`,
    }
  }

  defs(): ReactNode {
    return (
      // Vertical lines rotated 45°, which is cheaper than drawing diagonals
      // and tiles without seams. The stripe is the card's own colour rather
      // than a lighter tone of the fill: it reads as the bar being cut
      // through, and needs no second definition to follow the theme.
      <pattern
        key={this.id}
        id={this.id}
        width="6"
        height="6"
        patternUnits="userSpaceOnUse"
        patternTransform="rotate(45)"
      >
        {/* The shade goes through `style` over a plain `fill`, so a browser
            without color-mix drops the declaration and keeps the colour
            instead of painting the band with nothing. */}
        <rect width="6" height="6" fill={this.colour} style={{ fill: darker(this.colour) }} />
        <line
          x1="0"
          y1="0"
          x2="0"
          y2="6"
          stroke="var(--color-surface-container)"
          strokeWidth="2.5"
          opacity="0.55"
        />
      </pattern>
    )
  }

  shaded(): Paint {
    return this
  }
}

/**
 * The eight categorical fills, in fixed order.
 *
 * Validated as a set against the card surface — worst adjacent CVD dE 9.1,
 * normal-vision 19.6 — rather than picked one at a time. Defined in
 * styles/index.css and stepped again for the dark theme, so a chart follows
 * the theme without knowing either exists.
 */
export const SERIES_COLOURS = [
  'var(--color-series-1)',
  'var(--color-series-2)',
  'var(--color-series-3)',
  'var(--color-series-4)',
  'var(--color-series-5)',
  'var(--color-series-6)',
  'var(--color-series-7)',
  'var(--color-series-8)',
] as const

/** The fill for a bar that means a quantity rather than a thing. */
export const MAGNITUDE = new Solid('var(--color-primary)')

/**
 * Colour that follows the entity rather than its position in a list.
 *
 * The breakdowns arrive sorted by traffic, so indexing by row would repaint
 * every bar the moment the ranking changed — the same model blue this hour and
 * orange the next, which reads as something having happened. Sorting the names
 * gives each one a slot that moves only when the set of names does.
 */
export class Palette {
  private readonly slots: Map<string, number>

  constructor(labels: Iterable<string>) {
    this.slots = new Map([...new Set(labels)].sort().map((label, i) => [label, i]))
  }

  paint(label: string): Paint {
    const slot = this.slots.get(label) ?? 0
    return new Solid(SERIES_COLOURS[slot % SERIES_COLOURS.length])
  }
}

/** Every `<defs>` entry a set of paints needs, each one once. */
export function defsFor(paints: Iterable<Paint>): ReactNode[] {
  const seen = new Set<string>()
  const out: ReactNode[] = []
  for (const paint of paints) {
    if (paint instanceof Hatch) {
      if (seen.has(paint.id)) continue
      seen.add(paint.id)
    }
    const node = paint.defs()
    if (node !== null) out.push(node)
  }
  return out
}
