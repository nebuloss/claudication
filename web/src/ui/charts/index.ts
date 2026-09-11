/**
 * The charts, as a small library rather than a dependency.
 *
 * Hand-rolled for the same reason everything else here is: this UI is compiled
 * into a twelve-megabyte binary that ships it, and a charting library would be
 * a large fraction of the bundle for four pictures. What a library would buy —
 * scales, nice ticks, stacking, a legend that matches — is what these files
 * are, and they come to a few hundred lines.
 *
 * The split is deliberate. Geometry and paint are objects: a scale has a
 * domain, a range and several questions asked of it, and a paint has to answer
 * the same question in three places at once. Those are things with state worth
 * holding, and threading them through as bare arguments is how the arithmetic
 * ends up duplicated and then disagreeing with itself. Rendering stays
 * functions, because that is what React is.
 *
 *	scale.ts   LinearScale, BandScale — value and category to pixels
 *	paint.tsx  Solid, Hatch, Palette  — what a fill is and what it means
 *	stack.ts   Series, Stack          — the sums a stacked chart needs
 *	column.tsx ColumnChart, Legend    — a time series
 *	ranked.tsx RankedBars, CompositionBar — a league table, and shares of a whole
 *
 * Both the Overview and Usage screens are built on these, which is the point:
 * the rule about colour meaning identity, the stable slot per entity, and the
 * shaded-and-hatched treatment for a failed state are decided once here rather
 * than argued out again per screen.
 */
export { LinearScale, BandScale, niceTicks } from './scale'
export { Solid, Hatch, Palette, MAGNITUDE, SERIES_COLOURS, defsFor, type Paint } from './paint'
export { Stack, type Series } from './stack'
export { ColumnChart, Legend } from './column'
export { RankedBars, CompositionBar, type Rank } from './ranked'
