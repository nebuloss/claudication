// Exercise the chart library's arithmetic against the real compiled sources.
//
// Geometry is the part of a chart that can be silently wrong: one with a bad
// scale still draws, it just lies. This caught exactly that — niceTicks used
// to return a top gridline *below* the data for some maxima, which draws a
// column taller than the plot and out through the top of the chart.
//
// `make test-web` compiles the two pure modules and runs this. Only the pure
// ones: the React components need a DOM, and a headless browser to check that
// a rect landed where the scale said it would is a great deal of machinery to
// re-test arithmetic that is already checked here.

const OUT = process.env.OUT ?? new URL('../web/.charts-check', import.meta.url).pathname
const { niceTicks, LinearScale, LogScale, BandScale } = await import(`${OUT}/scale.js`)
const { Stack } = await import(`${OUT}/stack.js`)

let failures = 0
function check(what, got, want) {
  const ok = JSON.stringify(got) === JSON.stringify(want)
  if (!ok) failures++
  console.log(
    `${ok ? 'ok  ' : 'FAIL'}  ${what}` +
      (ok ? '' : `\n        got  ${JSON.stringify(got)}\n        want ${JSON.stringify(want)}`),
  )
}

console.log('— niceTicks: the top line has to be a number worth printing —')
check('0, because an empty chart still needs an axis', niceTicks(0), [0])
check('a negative max does not produce a negative axis', niceTicks(-5), [0])
check('7', niceTicks(7), [0, 2, 4, 6, 8])
// 25 is not on the 1/2/5 ladder, so 100 is covered in two steps rather than
// four. Fewer, rounder lines is the right answer; `count` is a ceiling.
check('100 lands exactly, with no phantom tick above it', niceTicks(100), [0, 50, 100])
// The bug this caught: the top tick used to land at 1000000, below the data,
// and a column taller than its axis is drawn out through the top of the plot.
check('the top tick covers the data', niceTicks(1183402).at(-1) >= 1183402, true)
check('and is still a round number', niceTicks(1183402).at(-1), 1500000)
check('1, a single request — fewer steps is allowed', niceTicks(1), [0, 0.5, 1])

console.log('\n— LinearScale: y0 is the pixel of zero, y1 the pixel of the top —')
const y = new LinearScale(80, 200, 0)
check('zero sits on the baseline', y.y(0), 200)
check('the top tick reaches the top', y.y(y.max), 0)
check('half the axis is half the pixels', y.height(y.max / 2), 100)
check('80 is already round, so the axis stops there', y.max, 80)
check('an empty axis does not divide by zero', new LinearScale(0, 200, 0).height(0), 0)

console.log('\n— LogScale: decades, and what happens to zero —')
const lg = new LogScale([3, 40, 900, 12000], 200, 0)
check('the floor is a decade below the smallest value', lg.bottom, 1)
check('the top is the decade above the largest', lg.max, 100000)
check('gridlines are whole decades', [...lg.ticks], [1, 10, 100, 1000, 10000, 100000])
check('the floor sits on the baseline', lg.y(lg.bottom), 200)
check('the top reaches the top', lg.y(lg.max), 0)
// Zero has no logarithm. Drawing it on the floor says "none" where a broken
// line would say "no data" — and with rarely-used keys there would be many.
check('zero is drawn on the floor, not off the chart', lg.y(0), lg.y(lg.bottom))
check('and so is anything below the floor', lg.y(0.001), lg.y(lg.bottom))
// Token counts start in the thousands; a floor of 1 would waste three decades.
const big = new LogScale([3000, 48000000], 200, 0)
check('the floor follows the data, not the origin', big.bottom, 1000)
check('a wide range still lands on decades', [...big.ticks].length, 6)
// An axis needs somewhere to go even when every value is the same.
const flat = new LogScale([50, 50, 50], 200, 0)
check('a single value still gets two gridlines', [...flat.ticks], [10, 100])
const nothing = new LogScale([0, 0], 200, 0)
check('all-zero does not divide by zero', Number.isFinite(nothing.y(0)), true)

console.log('\n— BandScale: a slot is what you point at, a bar is what is drawn —')
const x = new BandScale(4, 0, 400, { maxWidth: 34 })
check('slots tile the axis', [x.slot(0), x.slot(3)], [
  { x: 0, width: 100 },
  { x: 300, width: 100 },
])
check('the bar is capped and centred in its slot', x.bar(0), { x: 33, width: 34 })
const tight = new BandScale(60, 0, 400, { maxWidth: 34 })
check('60 columns still get a bar wider than nothing', tight.width > 0, true)
check('and never one wider than its slot', tight.width <= tight.step, true)

const many = new BandScale(30, 0, 400)
const shown = [...Array(30).keys()].filter(many.labelled(6))
check('30 columns yield at most 6 labels', shown.length <= 6, true)
check('the first is always labelled, to anchor the axis', shown[0], 0)

console.log('\n— Stack: the sums a stacked column needs —')
const data = [
  { ok: 10, bad: 2 },
  { ok: 0, bad: 0 },
  { ok: 30, bad: 0 },
]
const series = [
  { key: 'ok', label: 'ok', paint: null, value: (d) => d.ok },
  { key: 'bad', label: 'bad', paint: null, value: (d) => d.bad },
]
const stack = new Stack(data, series)
check('totals are per column', [...stack.totals], [12, 0, 30])
check('the peak is what the axis must cover', stack.peak, 30)
check('the sum is the headline', stack.sum, 42)
check('empty segments are skipped, not drawn at zero height', stack.segments(1).length, 0)
check('a column with one empty series yields one segment', stack.segments(2).length, 1)
check('segments come back bottom-up', stack.segments(0).map((s) => s.series.key), ['ok', 'bad'])
check('a series total spans every column', stack.seriesTotal(series[1]), 2)

const negative = new Stack([{ ok: -5, bad: 3 }], series)
check('a negative value cannot pull a column below zero', negative.peak, 3)

console.log('\n— every axis covers its data —')
for (const m of [1, 7, 42, 99, 100, 101, 999, 1183402, 5e9, 0.3]) {
  check(`top >= ${m}`, niceTicks(m).at(-1) >= m, true)
}

console.log(failures === 0 ? '\nPASS' : `\nFAIL (${failures})`)
process.exit(failures === 0 ? 0 : 1)
