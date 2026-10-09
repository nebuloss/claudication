// Exercise the UI's pure logic against the real compiled sources.
//
// Mostly the chart library's arithmetic, because geometry is the part of a
// chart that can be silently wrong: one with a bad scale still draws, it just
// lies. This caught exactly that — niceTicks used to return a top gridline
// *below* the data for some maxima, which draws a column taller than the plot
// and out through the top of the chart.
//
// And now the routing, for the same reason in a different shape: a URL that
// parses to the wrong tab still renders a screen, just not the one the link
// asked for.
//
// `make test-web` compiles the pure modules and runs this. Only the pure ones:
// the React components need a DOM, and a headless browser to check that a rect
// landed where the scale said it would is a great deal of machinery to re-test
// arithmetic that is already checked here.

const OUT = process.env.OUT ?? new URL('../web/.charts-check', import.meta.url).pathname
const { niceTicks, LinearScale, LogScale, BandScale, edgeAnchor } =
  await import(`${OUT}/charts/scale.js`)
const { Stack } = await import(`${OUT}/charts/stack.js`)
const { tabFromPath, pathForTab, panelFromHash } = await import(`${OUT}/route.js`)
const { Solid, Hatch, Line } = await import(`${OUT}/charts/paint.js`)

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

// The case that made the ranked bars useless, measured in production: one
// model held 1,935,946,849 tokens and the next 548,979. On a linear scale
// every row but the leader computed under 0.03% and was clamped to the 2%
// minimum, so four of five bars were the same length and the picture said
// nothing. Ranked bars run the scale over 0..100 because a bar is a CSS width.
// Normalised against the leader, as RankedBars does: LogScale rounds its top
// up to a whole decade, which an axis needs for its gridline and a list does
// not — unnormalised this leader sits at 92% against a 1e10 ceiling nothing
// reaches.
const spread = [1935946849, 548979, 187269, 106, 96]
const ranked = new LogScale(spread, 0, 100)
const top = ranked.y(Math.max(...spread))
const widths = spread.map((v) => Math.round((ranked.y(v) / top) * 100))
check('the leader fills the row', widths[0], 100)
check('the smallest is visible rather than clamped to the floor', widths[4] > 5, true)
check('three decades down is around half, not 0%', widths[1] > 40 && widths[1] < 70, true)
check('they descend with the data', [...widths].sort((a, b) => b - a), widths)
// 106 and 96 are near-equal and must look it. The bug was rows that differed
// by seven orders of magnitude looking identical, not rows that genuinely are.
check('near-equal rows stay near-equal', widths[3] - widths[4] <= 2, true)

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

console.log('\n— edgeAnchor: the outermost labels must not hang off the plot —')
// The bug: a point scale puts the last vertex ON the right edge, so a label
// centred there had half its text outside the viewBox and was clipped.
check('the last label is right-aligned', edgeAnchor(4, 5), 'end')
check('the first is left-aligned', edgeAnchor(0, 5), 'start')
check('the rest are centred', edgeAnchor(2, 5), 'middle')
check('a lone column is centred, not shoved left', edgeAnchor(0, 1), 'middle')
check('an empty axis does not throw', edgeAnchor(0, 0), 'middle')

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

console.log('\n— paint: a fill colours something, a swatch is a whole square —')
// The bug this exists for. A ranked bar computed its width from the scale and
// then spread swatch() over it — and swatch() is a legend square, carrying
// width: 10. Every bar rendered at 10px regardless of value, so the log scale
// above was correct and invisible, and two releases "fixed" nothing.
//
// The scale tests passed throughout, because they never looked at what reached
// the DOM. This checks the seam they missed.
for (const [what, paint] of [
  ['solid', new Solid('#2a78d6')],
  ['hatch', new Hatch('#2a78d6')],
  ['line', new Line('#2a78d6')],
]) {
  check(`${what}: a fill sets no width`, 'width' in paint.fill(), false)
  check(`${what}: a fill sets no height`, 'height' in paint.fill(), false)
  check(`${what}: its swatch still does`, 'width' in paint.swatch(), true)
}
// A bar keeps the width it computed once the paint is spread over it.
const barStyle = { width: '57%', ...new Solid('#2a78d6').fill() }
check('a bar keeps the width the scale gave it', barStyle.width, '57%')

console.log('\n— route: the screen is the path, the panel is the hash —')
const TABS = ['overview', 'setup', 'accounts', 'keys', 'usage', 'settings']
const PANELS = ['requests', 'chats', 'day', 'model', 'account', 'key']
const tab = (p) => tabFromPath(p, TABS, 'overview')
const panel = (h) => panelFromHash(h, PANELS, 'requests')

check('a plain screen', tab('/usage'), 'usage')
check('the root is the default screen', tab('/'), 'overview')
check('and so is an empty path', tab(''), 'overview')
// A URL is something people type, edit and truncate.
check('a trailing slash is the same screen', tab('/usage/'), 'usage')
check('a doubled slash is forgiven', tab('//usage'), 'usage')
check('a deeper path still names its screen', tab('/usage/whatever'), 'usage')
// A bookmark to a tab that has since been renamed lands somewhere sensible
// rather than on a blank screen.
check('an unknown screen falls back', tab('/nonesuch'), 'overview')

check('the default screen lives at the root, not at its own name', pathForTab('overview', 'overview'), '/')
check('every other screen is its name', pathForTab('usage', 'overview'), '/usage')
// Round trip: whatever pathForTab writes, tabFromPath has to read back.
check(
  'every tab survives the round trip',
  TABS.map((t) => tab(pathForTab(t, 'overview'))),
  TABS,
)

check('a panel', panel('#chats'), 'chats')
check('no hash is the default panel', panel(''), 'requests')
check('a bare hash is too', panel('#'), 'requests')
// A link that has been through a mail client comes back encoded.
check('a percent-encoded hash still opens its panel', panel('%23model'), 'model')
check('the panel is the first of several hash parts', panel('#model&x=1'), 'model')
check('an unknown panel falls back', panel('#nonesuch'), 'requests')
// A malformed escape must not throw inside a render.
check('a broken escape does not throw', panel('#%E0%A4%A'), 'requests')

// The client configs handed out are the committed files with their models
// rewritten from the gateway's live list. Checked against the real committed
// files, because the rewrite is only as good as its reading of them.
console.log('\n— model catalog: the configs are written from the live list —')
{
  const {
    Catalog,
    CRUSH_WINDOW,
    tailorCrush,
    tailorCodexCatalog,
    tailorCodexConfig,
    tailorClaudeCode,
    tailorOpencode,
  } = await import(`${OUT}/features/docs/catalog.js`)
  const { readFileSync } = await import('node:fs')
  const committed = (name) =>
    readFileSync(new URL(`../configs/clients/${name}`, import.meta.url), 'utf8')

  const effort = (...levels) => ({
    supported: true,
    ...Object.fromEntries(['low', 'medium', 'high', 'xhigh', 'max'].map((l) => [l, { supported: levels.includes(l) }])),
  })
  // Shaped as GET /v1/models answered on 2026-10-09, newest first.
  const live = [
    { id: 'claude-haiku-5-5', display_name: 'Claude Haiku 5.5', line: 'haiku', lifecycle: 'active',
      max_input_tokens: 1000000, max_tokens: 128000,
      capabilities: { effort: effort('low', 'medium', 'high', 'xhigh', 'max'), image_input: { supported: true } } },
    { id: 'claude-opus-5-5', display_name: 'Claude Opus 5.5', line: 'opus', lifecycle: 'active',
      max_input_tokens: 1000000, max_tokens: 128000,
      capabilities: { effort: effort('low', 'medium', 'high', 'xhigh', 'max'), image_input: { supported: true } } },
    { id: 'claude-opus-4-5-20251101', display_name: 'Claude Opus 4.5', line: 'opus', lifecycle: 'active',
      max_input_tokens: 200000, max_tokens: 64000,
      capabilities: { effort: effort('low', 'medium', 'high'), image_input: { supported: true } } },
    { id: 'claude-sonnet-4-5-20250929', display_name: 'Claude Sonnet 4.5', lifecycle: 'active',
      max_input_tokens: 200000, max_tokens: 64000,
      capabilities: { effort: { supported: false }, image_input: { supported: true } } },
    { id: 'claude-sonnet-3-9', lifecycle: 'deprecated', max_input_tokens: 200000 },
    { id: 'gpt-something' },
  ]
  const catalog = new Catalog(live)

  check('retired and foreign models are left out', catalog.models.map((m) => m.id), [
    'claude-haiku-5-5', 'claude-opus-5-5', 'claude-opus-4-5-20251101', 'claude-sonnet-4-5-20250929',
  ])
  check('the large default is the newest Opus', catalog.large, 'claude-opus-5-5')
  check('the small default is the newest Haiku', catalog.small, 'claude-haiku-5-5')
  check('an empty list writes nothing', new Catalog(null).known, false)

  const crush = JSON.parse(tailorCrush(committed('crush.json'), catalog))
  const crushModels = Object.values(crush.providers)[0].models
  const byID = Object.fromEntries(crushModels.map((m) => [m.id, m]))
  check('crush gets every live model', crushModels.map((m) => m.id), catalog.models.map((m) => m.id))
  check('a 1M model is declared at the compaction window', byID['claude-opus-5-5'].context_window, CRUSH_WINDOW)
  check('a 200k model keeps its own window', byID['claude-opus-4-5-20251101'].context_window, 200000)
  check('effort levels are the ones the model accepts', byID['claude-opus-4-5-20251101'].reasoning_levels, ['low', 'medium', 'high'])
  check('a model with no effort setting does not reason', [byID['claude-sonnet-4-5-20250929'].can_reason, 'reasoning_levels' in byID['claude-sonnet-4-5-20250929']], [false, false])
  check('the output cap is the model\'s', byID['claude-opus-5-5'].default_max_tokens, 128000)
  check('crush defaults follow', [crush.models.large.model, crush.models.small.model], ['claude-opus-5-5', 'claude-haiku-5-5'])
  check('the rest of crush.json is the committed file\'s', [crush.$schema, Object.keys(crush.providers)[0], crush.models.large.think],
    [JSON.parse(committed('crush.json')).$schema, 'claudication', true])

  const codexCommitted = JSON.parse(committed('codex-models.json'))
  const codex = JSON.parse(tailorCodexCatalog(committed('codex-models.json'), catalog)).models
  check('codex gets every live model, in order', codex.map((m) => [m.slug, m.priority]),
    catalog.models.map((m, i) => [m.id, (i + 1) * 10]))
  check('codex windows are the real ones', [codex[1].context_window, codex[2].max_context_window], [1000000, 200000])
  check('everything else in a codex entry is the committed one', codex[1].base_instructions, codexCommitted.models[0].base_instructions)

  const toml = tailorCodexConfig(committed('codex.toml'), catalog)
  check('codex.toml names the new default', /^model = "claude-opus-5-5"$/m.test(toml), true)
  check('and nothing else in it moves', toml.replace('claude-opus-5-5', 'X'), committed('codex.toml').replace(/^model = "[^"]*"/m, 'model = "X"'))

  const sh = tailorClaudeCode(committed('claude-code.sh'), catalog)
  check('Claude Code gets both defaults', [/^export ANTHROPIC_MODEL=claude-opus-5-5$/m.test(sh), /^export ANTHROPIC_SMALL_FAST_MODEL=claude-haiku-5-5$/m.test(sh)], [true, true])

  const oc = tailorOpencode(committed('opencode.jsonc'), catalog)
  check('opencode gets both defaults, quotes intact', [oc.includes('"model": "anthropic/claude-opus-5-5"'), oc.includes('"small_model": "anthropic/claude-haiku-5-5"')], [true, true])
  check('opencode keeps its comments', oc.includes('// opencode, pointed at a claudication gateway.'), true)
}

console.log('\n— every axis covers its data —')
for (const m of [1, 7, 42, 99, 100, 101, 999, 1183402, 5e9, 0.3]) {
  check(`top >= ${m}`, niceTicks(m).at(-1) >= m, true)
}

console.log(failures === 0 ? '\nPASS' : `\nFAIL (${failures})`)
process.exit(failures === 0 ? 0 : 1)
