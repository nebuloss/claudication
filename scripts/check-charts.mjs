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
// And each client type's files, written from the committed ones: a config
// that parses but names the wrong model still installs, it just asks the
// gateway for a name the client does not have.
//
// `make test-web` compiles the pure modules and runs this. Only the pure ones:
// the React components need a DOM, and a headless browser to check that a rect
// landed where the scale said it would is a great deal of machinery to re-test
// arithmetic that is already checked here.

// tsc leaves a relative import as it was written, without the .js an ES
// module needs, so a pure module that imports another (the request filter
// builds its paths with route.ts's pathOf) would not load here as compiled.
// The browser never sees these files — Vite resolves them — so the extension
// is added here rather than written into the sources.
import { register } from 'node:module'
register(
  'data:text/javascript,' +
    encodeURIComponent(
      'export async function resolve(s, c, next) {' +
        " if (s.startsWith('.') && !/\\.[cm]?js$/.test(s)) return next(s + '.js', c);" +
        ' return next(s, c) }',
    ),
)

const OUT = process.env.OUT ?? new URL('../web/.charts-check/ui', import.meta.url).pathname
const { niceTicks, LinearScale, LogScale, BandScale, edgeAnchor } =
  await import(`${OUT}/charts/scale.js`)
const { Stack } = await import(`${OUT}/charts/stack.js`)
const { canonical, tabOf, segments, pathOf, navLevel, trailOf } = await import(
  `${OUT}/route.js`
)
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

console.log('\n— route: every page is a path, and the path is the tree —')
const tab = (p) => tabOf(canonical(p))

check('the root is the overview', canonical('/'), '/')
check('and so is an empty path', canonical(''), '/')
check('the overview lives at the root, not at its own name', canonical('/overview'), '/')
// A URL is something people type, edit and truncate.
check('a trailing slash is the same page', canonical('/accounts/'), '/accounts')
check('a doubled slash is forgiven', canonical('//clients'), '/clients')
check('settings is its own general view', canonical('/settings'), '/settings')
check('its pages are pages', ['models', 'configuration'].map((p) => canonical(`/settings/${p}`)),
  ['/settings/models', '/settings/configuration'])
check('general and MCP are not pages: they are Settings itself', ['general', 'mcp'].map((p) => canonical(`/settings/${p}`)),
  ['/settings', '/settings'])
check('a setting it does not have lands on Settings', canonical('/settings/images'), '/settings')
check('an unknown screen falls back', canonical('/nonesuch'), '/')
// Screens that went away are not redirected: their bookmarks land on the
// overview, rather than on a page that only looks like the one they named.
check('the old key list is not a screen', canonical('/keys'), '/')
check('nor is a key', canonical('/keys/k1/usage'), '/')
check('the APIs section went with the per-API switches', canonical('/settings/apis'), '/settings')
check('and the aliases with the global names', canonical('/settings/aliases'), '/settings')

// An item is its own overview, with its traffic and its settings as pages
// under it: two levels, never a third.
check('an account is its own overview', canonical('/accounts/8189e5dc'), '/accounts/8189e5dc')
check('an overview link lands on it', canonical('/accounts/8189e5dc/overview'), '/accounts/8189e5dc')
check('a setting is a page', canonical('/accounts/a1/connection'), '/accounts/a1/connection')
check('an account has no chats: a chat is a client\'s', canonical('/accounts/a1/chats'), '/accounts/a1')
check('a client has', canonical('/clients/c1/chats'), '/clients/c1/chats')
check('and its usage, limits beside it', canonical('/clients/c1/usage'), '/clients/c1/usage')
check('an unknown page is its overview', canonical('/clients/c1/nonesuch'), '/clients/c1')
check('activity is its own overview', canonical('/activity'), '/activity')
check('its tables are pages', canonical('/activity/models'), '/activity/models')
check('the request log too', canonical('/activity/requests'), '/activity/requests')
check('chats are not on activity', canonical('/activity/chats'), '/activity')
// Its overview is its own path, not a page named overview under it.
check('nor an overview page', canonical('/activity/overview'), '/activity')
// Ids are encoded once, whatever they hold.
check('an id is kept encoded', canonical('/clients/a%2Fb'), '/clients/a%2Fb')
check('and decoded when read', segments('/clients/a%2Fb/usage'), ['clients', 'a/b', 'usage'])
check('a broken escape does not throw', segments('/clients/%E0%A4%A'), ['clients', '%E0%A4%A'])
check('a path is built encoded', pathOf('accounts', 'a b', 'usage'), '/accounts/a%20b/usage')

check('a page names its screen', tab('/accounts/8189e5dc/connection'), 'accounts')
check('a client page too', tab('/clients/c1/requests'), 'clients')
// Round trip: every canonical path is its own canonical form.
{
  const fixed = ['/', '/accounts', '/accounts/x', '/accounts/x/usage', '/clients/x/models', '/activity/models', '/activity/accounts', '/activity/requests', '/settings', '/settings/models']
  check('canonical paths are fixed points', fixed.map((p) => canonical(p)), fixed)
}

// The rail lists only the level a page is on, with the way up; the trail
// lists every level. Names come from the pages, by id.
{
  const name = (kind, id) => `${kind}:${id}`
  const lvl = (p) => navLevel(canonical(p))
  const hrefs = (l) => l.items.map((i) => i.href)

  check('the top level has no way up', lvl('/').back, undefined)
  // The overview is the root itself, reached by the trail's first step.
  check('it lists the screens', lvl('/').items.map((i) => i.label), ['Accounts', 'Clients', 'Activity', 'Settings'])
  check('a list is on the top level', lvl('/clients').current, '/clients')
  check('activity opens on its overview', hrefs(lvl('/'))[2], '/activity')
  check('activity lists its four pages', hrefs(lvl('/activity/clients')), [
    '/activity/models', '/activity/clients', '/activity/accounts', '/activity/requests',
  ])
  check('and the page open is current', lvl('/activity/clients').current, '/activity/clients')
  check('an account lists its pages in a client\'s order', hrefs(lvl('/accounts/a1/usage')), [
    '/accounts/a1/usage', '/accounts/a1/models',
    '/accounts/a1/requests', '/accounts/a1/connection', '/accounts/a1/advanced',
  ])
  check('a client too', hrefs(lvl('/clients/c1/usage')), [
    '/clients/c1/usage', '/clients/c1/models', '/clients/c1/chats', '/clients/c1/requests',
  ])
  check('on its overview none is current', lvl('/accounts/a1').current, '/accounts/a1')
  check('a setting is current like any page', lvl('/clients/c1/models').current, '/clients/c1/models')
  check('and goes back to the list', lvl('/clients/c1/usage').back, { href: '/clients', label: 'Clients' })
  // Settings lists its pages, as Activity does, with the way back.
  check('settings opens on its general view from the top level', hrefs(lvl('/'))[3], '/settings')
  check('settings lists its pages', hrefs(lvl('/settings/models')), ['/settings/models', '/settings/configuration', '/settings/diagnostics'])
  check('the page open is current', lvl('/settings/models').current, '/settings/models')
  check('nothing is current on its general view', lvl('/settings').current, '/settings')
  check('and goes back to the main menu', lvl('/settings/models').back?.href, '/')

  const trail = (p) => trailOf(canonical(p), name).map((c) => c.label)
  // Every trail starts at the root, and no level is named twice.
  check('the overview is the root alone', trail('/'), ['Overview'])
  check('a list', trail('/clients'), ['Overview', 'Clients'])
  check('settings is its own step', trail('/settings'), ['Overview', 'Settings'])
  check('a settings page', trail('/settings/models'), ['Overview', 'Settings', 'Models'])
  check('a client is its name, not its name then Overview', trail('/clients/c1'), ['Overview', 'Clients', 'clients:c1'])
  check('a page of an account', trail('/accounts/a1/connection'), ['Overview', 'Accounts', 'accounts:a1', 'Connection'])
  check('an activity page', trail('/activity/requests'), ['Overview', 'Activity', 'Requests'])
  // Its overview is Activity itself, so the trail stops there.
  check('activity is its own step', trail('/activity'), ['Overview', 'Activity'])
  check('nothing is current on its overview', lvl('/activity').current, '/activity')
  check('the item step is its overview', trailOf('/clients/c1/usage', name)[2].href, '/clients/c1')
}

console.log('\n— client specs: a slot\'s binding is one menu value —')
{
  const { Binding, BindingMenu } = await import(`${OUT}/clients/specs.js`)
  check('a model round-trips', Binding.parse(Binding.model('claude-opus-5-5').value).body, { model: 'claude-opus-5-5' })
  check('a family too', Binding.parse(Binding.follow('claude:opus').value).body, { follows: 'claude:opus' })
  check('a model id with a colon survives', Binding.parse('model:a:b').model, 'a:b')
  check('a slot reads as its binding', Binding.of({ model: '', bound: 'openai:sol' }).value, 'follow:openai:sol')

  // By family: the menu is the families, and a model no family takes.
  const byFamily = [
    { follows: 'claude:opus', family: 'claude-opus', title: 'Claude Opus', serving: 'claude-opus-5-5', provider: 'anthropic',
      versions: ['claude-opus-5-5', 'claude-opus-5'] },
    { follows: 'openai:sol', family: 'gpt-sol', title: 'GPT Sol', serving: 'gpt-6.1-sol', provider: 'openai', versions: ['gpt-6.1-sol'] },
    { model: 'codex-x', title: 'Codex X', serving: 'codex-x', provider: 'openai' },
  ]
  const fam = new BindingMenu(byFamily, Binding.follow('claude:opus'), { title: 'Claude Opus', serving: 'claude-opus-5-5' })
  check('each choice once, by provider, nothing else', fam.options.map((o) => [o.group, o.value, o.label]), [
    ['Claude', 'follow:claude:opus', 'claude-opus (claude-opus-5-5)'], ['ChatGPT', 'follow:openai:sol', 'gpt-sol (gpt-6.1-sol)'], ['ChatGPT', 'model:codex-x', 'Codex X'],
  ])
  check('a family says what an account without its newest gets', fam.options[0].hint,
    'or claude-opus-5 on an account without it')
  check('and nothing when every account has the same', fam.options[1].hint, '')
  check('the binding is the value', fam.value, 'follow:claude:opus')
  check('picking binds what was picked', fam.pick('follow:openai:sol').body, { follows: 'openai:sol' })
  check('a new name starts on the Opus family', BindingMenu.initial(byFamily).body, { follows: 'claude:opus' })
  check('or on the first choice there is', BindingMenu.initial(byFamily.slice(1)).body, { follows: 'openai:sol' })
  check('or a model', BindingMenu.initial(byFamily.slice(2)).body, { model: 'codex-x' })

  // Every model: the menu is the models.
  const every = [
    { model: 'claude-opus-5-5', title: 'Claude Opus 5.5', serving: 'claude-opus-5-5', provider: 'anthropic' },
    { model: 'gpt-6.1-sol', title: 'GPT-6.1-Sol', serving: 'gpt-6.1-sol', provider: 'openai' },
  ]
  const plain = new BindingMenu(every, Binding.model('gpt-6.1-sol'))
  check('every model: each model, by name with its id as the hint', plain.options.map((o) => [o.group, o.value, o.hint]), [
    ['Claude', 'model:claude-opus-5-5', 'claude-opus-5-5'], ['ChatGPT', 'model:gpt-6.1-sol', 'gpt-6.1-sol'],
  ])

  // A binding the menu does not offer stays visible, first, not choosable.
  const other = new BindingMenu(every, Binding.follow('claude:opus'), { title: 'Claude Opus', serving: 'claude-opus-5-5' })
  check('a family bound before every model was offered: shown, marked',
    [other.value, other.options[0].value, other.options[0].group, other.options[0].disabled, other.options.length],
    ['follow:claude:opus', 'follow:claude:opus', 'Bound now', true, 3])
  const kept = new BindingMenu(byFamily, Binding.model('claude-haiku-4-5'), { title: 'claude-haiku-4-5', serving: 'claude-haiku-4-5', unavailable: 'no connected account lists it' })
  check('an unavailable binding says why', [kept.options[0].hint, kept.options[0].disabled],
    ['unavailable: no connected account lists it', true])
}
console.log('\n— model tree: provider, family, model, newest first —')
{
  const { modelTree, familyOf, modelNodes, newestOnly, changes } = await import(`${OUT}/domain/model-tree.js`)
  const { defaultOpen, shownLeaves } = await import(`${OUT}/primitives/check-tree-state.js`)
  const modelsOf = (t) => t.flatMap((p) => p.families.flatMap((f) => f.models))
  const nodesOf = (t) => modelNodes(t, (m) => ({ key: m.id, label: m.display_name ?? m.id, search: m.id }))
  // The ChatGPT backend's list as measured (2026-10-19), in its own ranking,
  // undated; Anthropic's dated, one by its line and the rest by id.
  const gpt = ['gpt-6.1-sol', 'gpt-6-astra', 'gpt-6-sol', 'gpt-6-luna', 'gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna']
    .map((id, i) => ({ id, provider: 'openai', priority: i + 1 }))
  const claude = [
    { id: 'claude-haiku-4-5-20251001', created_at: '2025-10-01T00:00:00Z' },
    { id: 'claude-opus-5', created_at: '2026-03-01T00:00:00Z' },
    { id: 'claude-opus-5-5', created_at: '2026-09-01T00:00:00Z', line: 'Claude Opus' },
    { id: 'claude-sonnet-5', created_at: '2026-06-01T00:00:00Z' },
    { id: 'claude-fable-1', created_at: '2026-10-01T00:00:00Z' },
  ]
  const tree = modelTree([...gpt, ...claude])
  check('Claude first, then ChatGPT', tree.map((p) => [p.key, p.title]), [['anthropic', 'Claude'], ['openai', 'ChatGPT']])
  check('Claude by line, the family with the newest release first',
    tree[0].families.map((f) => [f.title, f.models.map((m) => m.id)]), [
      ['Fable', ['claude-fable-1']],
      ['Opus', ['claude-opus-5-5', 'claude-opus-5']],
      ['Sonnet', ['claude-sonnet-5']],
      ['Haiku', ['claude-haiku-4-5-20251001']],
    ])
  check('ChatGPT by generation, newest first, each in the backend\'s ranking',
    tree[1].families.map((f) => [f.title, f.models.map((m) => m.id)]), [
      ['GPT-6.1', ['gpt-6.1-sol']],
      ['GPT-6', ['gpt-6-astra', 'gpt-6-sol', 'gpt-6-luna']],
      ['GPT-5.6', ['gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna']],
    ])
  check('an id with no family is Other, and last', modelTree([{ id: 'codex-auto' , provider: 'openai' }, gpt[0]])[0].families.map((f) => f.title),
    ['GPT-6.1', 'Other'])
  check('a line wins over the id', familyOf({ id: 'claude-x-1', line: 'opus' }).key, 'opus')
  check('undated, a later version is newer', modelTree([{ id: 'claude-opus-5' }, { id: 'claude-opus-5-5' }])[0].families[0].models.map((m) => m.id),
    ['claude-opus-5-5', 'claude-opus-5'])
  check('as nodes: provider over family over model',
    nodesOf(tree).map((p) => [p.label, p.children.map((f) => [f.label, f.children.map((m) => m.key)])])[0].slice(0, 2),
    ['Claude', [['Fable', ['claude-fable-1']], ['Opus', ['claude-opus-5-5', 'claude-opus-5']], ['Sonnet', ['claude-sonnet-5']],
      ['Haiku', ['claude-haiku-4-5-20251001']]]])
  check('open at first: each provider and its newest family', [...defaultOpen(nodesOf(tree))],
    ['anthropic', 'anthropic/fable', 'openai', 'openai/gpt-6.1'])
  const named = nodesOf(modelTree([{ id: 'claude-opus-5-5', display_name: 'Claude Opus 5.5' },
    { id: 'claude-opus-5', display_name: 'Claude Opus 5' }, { id: 'claude-sonnet-5', display_name: 'Claude Sonnet 5' }, ...gpt]))
  check('a search matches every word, in name or id, any case', shownLeaves(named, 'OPUS 5.5'), ['claude-opus-5-5'])
  check('an id pasted whole finds its row', shownLeaves(named, 'gpt-5.6-terra'), ['gpt-5.6-terra'])
  check('an empty search is the whole tree', shownLeaves(named, '  ').length, 10)
  const want = newestOnly(tree)
  check('newest only: the first of each family on, the rest off',
    modelsOf(tree).filter((m) => want.get(m.id)).map((m) => m.id),
    ['claude-fable-1', 'claude-opus-5-5', 'claude-sonnet-5', 'claude-haiku-4-5-20251001', 'gpt-6.1-sol', 'gpt-6-astra', 'gpt-5.6-sol'])
  const now = new Set(['claude-opus-5-5', 'claude-opus-5', 'gpt-6-sol'])
  check('a preset writes only what differs, in order',
    changes(modelsOf(tree), (m) => now.has(m.id), (m) => want.get(m.id)).map((c) => `${c.model.id}=${c.enabled}`),
    ['claude-fable-1=true', 'claude-opus-5=false', 'claude-sonnet-5=true', 'claude-haiku-4-5-20251001=true',
      'gpt-6.1-sol=true', 'gpt-6-astra=true', 'gpt-6-sol=false', 'gpt-5.6-sol=true'])
  const offered = modelTree([{ id: 'claude-opus', follows: 'claude:opus' }, { id: 'claude-haiku', follows: 'claude:haiku' },
    { id: 'gpt-sol', follows: 'openai:sol', provider: 'openai' }, { id: 'codex-x', provider: 'openai' }])
  check('families offered as one are rows of their own under their provider, in the server\'s order',
    nodesOf(offered).map((p) => [p.label, p.children.map((c) => c.children === undefined ? c.key : [c.label, c.children.map((m) => m.key)])]),
    [['Claude', ['claude-opus', 'claude-haiku']], ['ChatGPT', ['gpt-sol', ['Other', ['codex-x']]]]])
  check('and newest only leaves them be', [...newestOnly(offered).keys()], ['codex-x'])
  check('a model the preset does not name is left alone',
    changes([{ id: 'a' }, { id: 'b' }], () => false, (m) => (m.id === 'a' ? true : undefined)).map((c) => c.model.id), ['a'])
}

console.log('\n— check tree: depth, boxes and counts worked out from the nodes —')
{
  const { place, cut, boxState, defaultOpen, leafDepth, leafKeys, shownLeaves } = await import(`${OUT}/primitives/check-tree-state.js`)
  // Four levels deep, on purpose: the tree is not written for three.
  const nodes = [
    { key: 'a', label: 'A', defaultOpen: true, children: [
      { key: 'a1', label: 'A1', defaultOpen: true, children: [
        { key: 'a1x', label: 'A1X', children: [{ key: 'deep', label: 'Deep one', checked: true }] },
        { key: 'x', label: 'Ex', search: 'model-x', checked: true },
        { key: 'y', label: 'Why', checked: false },
      ] },
      { key: 'a2', label: 'A2', children: [{ key: 'z', label: 'Zed', checked: false, disabled: true }] },
    ] },
    { key: 'flat', label: 'Flat leaf', checked: true },
  ]
  const open = defaultOpen(nodes)
  const placed = place(nodes, (k) => open.has(k))
  const walk = (ps) => ps.flatMap((p) => [[p.node.key, p.depth, p.state, p.count], ...walk(p.children)])
  check('depth from nesting, any depth; a group\'s box and count from every leaf under it', walk(placed), [
    ['a', 0, 'mixed', '2 of 4'], ['a1', 1, 'mixed', '2 of 3'], ['a1x', 2, 'on', '1 of 1'], ['deep', 3, 'on', '1 of 1'],
    ['x', 2, 'on', '1 of 1'], ['y', 2, 'off', '0 of 1'], ['a2', 1, 'off', '0 of 1'], ['z', 2, 'off', '0 of 1'],
    ['flat', 0, 'on', '1 of 1'],
  ])
  check('open: the ones that say so', placed.map((p) => [p.node.key, p.open]).concat(placed[0].children.map((p) => [p.node.key, p.open])),
    [['a', true], ['flat', false], ['a1', true], ['a2', false]])
  check('a group\'s box moves every leaf under it but a disabled one', placed[0].leaves, ['deep', 'x', 'y'])
  check('a group of only disabled leaves is disabled itself', placed[0].children[1].disabled, true)
  check('a leaf\'s box is itself', placed[1].leaves, ['flat'])
  check('on, off or mixed — and off with nothing in it', [boxState([true, true]), boxState([false]), boxState([true, false]), boxState([])],
    ['on', 'off', 'mixed', 'off'])
  check('an empty group counts nothing and moves nothing',
    [place([{ key: 'g', label: 'G', children: [] }], () => true)].map(([g]) => [g.count, g.state, g.disabled, g.leaves]), [['0 of 0', 'off', true, []]])
  check('the deepest leaf', [leafDepth(nodes), leafDepth([{ key: 'l', label: 'L' }])], [3, 0])
  check('every leaf, in order', leafKeys(nodes), ['deep', 'x', 'y', 'z', 'flat'])
  const found = place(nodes, () => false, 'why')
  check('a search: its matches, their groups open whatever the folds, the rest gone',
    walk(found).map(([k]) => k).concat(found.map((p) => p.open)), ['a', 'a1', 'y', true])
  check('under a search a group boxes and counts what it shows', [found[0].state, found[0].count, found[0].leaves], ['off', '0 of 1', ['y']])
  check('the search words: label or search text, every word, any case', shownLeaves(nodes, 'MODEL x'), ['x'])
  check('a group is not matched by its own label', cut(nodes, 'A2').length, 0)
  check('no search, everything', shownLeaves(nodes, ' ').length, 5)
}

console.log('\n— model tests: a few at a time, each result as it lands —')
{
  const { eachBounded, explain, brief, failing } = await import(`${OUT}/domain/model-tests.js`)
  let running = 0
  let peak = 0
  const landed = []
  const delays = { a: 30, b: 5, c: 5, d: 5 }
  await eachBounded(['a', 'b', 'c', 'd'], 2, async (m) => {
    running++
    peak = Math.max(peak, running)
    await new Promise((r) => setTimeout(r, delays[m]))
    running--
    return m.toUpperCase()
  }, (m, r) => landed.push(`${m}${r}`))
  check('never more than the bound at once', peak, 2)
  check('a slow one does not hold the rest up', landed, ['bB', 'cC', 'dD', 'aA'])
  let none = 0
  await eachBounded([], 2, async () => none++, () => {})
  check('an empty list runs nothing', none, 0)
  const t = (ok, status, error) => ({ model: 'm', at: '', ok, status, latency_ms: 120, error })
  check('a pass says how long', explain(t(true, 200)), 'ok · 120 ms')
  check('a model outside the plan says so', explain(t(false, 400, 'usage_not_included')), 'not in this plan')
  check('an unknown model too', explain(t(false, 404, '')), 'not served to this account')
  check('a short reason fits the chip as it is', brief(t(false, 404, '')), 'not served to this account')
  const long = 'request failed: Post "https://api.anthropic.com/v1/messages": dial tcp: connection refused'
  check('a long one is cut to its kind, with no answer', brief(t(false, 0, long)), 'could not reach it')
  check('or its status', brief(t(false, 500, long)), 'failed (HTTP 500)')
  check('a rate limit passes', failing([{ id: 'a', enabled: true, test: t(false, 429) }]), [])
  check('a lasting failure on an enabled model is offered', failing([
    { id: 'a', enabled: true, test: t(false, 404) }, { id: 'b', enabled: false, test: t(false, 404) },
    { id: 'c', enabled: true, test: t(true, 200) }, { id: 'd', enabled: true },
  ]), ['a'])
}

console.log('\n— RequestFilter: the URL is the filter, and survives the trip —')
{
  const { RequestFilter, requestsHref } = await import(`${OUT}/features/requests/filter.js`)
  // The bug the class was made for: the parser knew only "failed", so the
  // Status menu's "Successful only" was dropped on reload and on Back.
  check('successful only survives a reload', RequestFilter.fromSearch('?status=ok').status, 'ok')
  check('failed only too', RequestFilter.fromSearch('?status=failed').status, 'failed')
  check('an edited status reads as unfiltered', RequestFilter.fromSearch('?status=nope').status, '')
  const f = RequestFilter.fromSearch('?model=a&model=b&model=&ip=10.0.0.1')
  check('a set is a repeated parameter, empty member and all', f.values('model'), ['a', 'b', ''])
  check('and goes back out the way it came', f.query(), 'model=a&model=b&model=&ip=10.0.0.1')
  check('a tick toggles one value', f.toggled('model', 'a').values('model'), ['b', ''])
  check('and back', f.toggled('model', 'a').toggled('model', 'a').values('model'), ['b', '', 'a'])
  check('changing one leaves the original alone', f.values('model'), ['a', 'b', ''])
  check('the outcome toggles off', RequestFilter.none.toggledStatus('ok').toggledStatus('ok').status, '')
  check('the outcome funnels the Status column', RequestFilter.none.withStatus('failed').narrows('code'), true)
  check('one account opens its own Requests page',
    requestsHref({ account: ['acc 1'], status: 'failed' }), '/accounts/acc%201/requests?status=failed')
  check('one client too', requestsHref({ key: ['k'] }), '/clients/k/requests')
  check('both stay on Activity', requestsHref({ key: ['k'], account: ['a'] }), '/activity/requests?key=k&account=a')
  const scope = { key: 'k' }
  const scoped = RequestFilter.of({ model: ['m'] }).scopedTo(scope)
  check('a scope is applied', scoped.values('key'), ['k'])
  check('and never written into the query', scoped.outside(scope).pathIn(scope), '/clients/k/requests?model=m')
}

console.log('\n— RequestLogPager: polling must not undo paging —')
{
  const { RequestLogPager, PAGE } = await import(`${OUT}/features/requests/pager.js`)
  const at = (m) => ({ id: m, at: new Date(Date.UTC(2026, 9, 19, 12, m)).toISOString() })
  const held = RequestLogPager.first({ rows: [at(5), at(4)], nextCursor: 'c1' }).more({ rows: [at(3)], nextCursor: 'c2' })
  check('load more appends and moves the cursor', [held.rows.length, held.cursor], [3, 'c2'])
  const polled = held.merged({ rows: [at(7), at(6), at(5), at(4)], nextCursor: 'p' })
  check('a poll adds only what is newer, on top', polled.rows.map((r) => r.at.slice(14, 16)), ['07', '06', '05', '04', '03'])
  check('and keeps the paging cursor', polled.cursor, 'c2')
  check('a quiet poll changes nothing', held.merged({ rows: [at(5)], nextCursor: 'p' }), held)
  const sameSecond = { id: 8, at: at(5).at }
  check('a row from the same second as the newest held is still new',
    held.merged({ rows: [sameSecond, at(5)], nextCursor: 'p' }).rows.map((r) => r.id), [8, 5, 4, 3])
  const flood = Array.from({ length: PAGE }, (_, i) => at(10 + i)).reverse()
  const over = held.merged({ rows: flood, nextCursor: 'p' })
  check('a whole page of new rows starts over rather than leave a hole', [over.rows.length, over.cursor], [PAGE, 'p'])
}

console.log('\n— commitTitle: what Enter in a title being renamed does —')
{
  const { commitTitle, MAX_NAME } = await import(`${OUT}/primitives/title-edit.js`)
  const client = { clearable: false }
  const account = { clearable: true }
  check('the same name, spaces aside, sends nothing', commitTitle('  Laptop ', 'Laptop', client), { kind: 'unchanged' })
  check('a new name is sent trimmed', commitTitle(' Desk ', 'Laptop', client), { kind: 'save', value: 'Desk' })
  check('a client cannot be left nameless', commitTitle('   ', 'Laptop', client).kind, 'invalid')
  check("an account's empty name is its address back", commitTitle('', 'Work', account), { kind: 'save', value: '' })
  check('an unnamed account left empty sends nothing', commitTitle(' ', '', account), { kind: 'unchanged' })
  check('the limit is the server\'s', commitTitle('x'.repeat(MAX_NAME), '', client).kind, 'save')
  check('one over is refused before it is sent', commitTitle('x'.repeat(MAX_NAME + 1), '', client).kind, 'invalid')
  check('counted in characters, as the server counts', commitTitle('🙂'.repeat(MAX_NAME), '', client).kind, 'save')
  check('a pasted newline is refused', commitTitle('two\nlines', '', client).kind, 'invalid')
}

console.log('\n— rollup: every usage table is the same rows folded —')
{
  const { rollup, tokensOf, byTokens, withoutAccount, percentOf } = await import(`${OUT}/features/usage/rollup.js`)
  const row = (client_id, account_id, slot, model, requests, last, extra = {}) => ({
    client_id, client: client_id, type: 'crush', account_id, slot, model, provider: 'anthropic',
    requests, errors: 0, input_tokens: requests * 10, output_tokens: requests, cache_tokens: 0,
    reasoning_tokens: 0, last, ...extra,
  })
  const rows = [
    row('a', 'acc1', 'large', 'opus', 3, '2026-10-20T10:00:00Z'),
    row('b', 'acc1', 'large', 'opus', 2, '2026-10-20T11:00:00Z', { errors: 1 }),
    row('a', 'acc2', 'large', 'opus', 2, '2026-10-20T09:00:00Z'),
    row('a', 'acc2', 'small', 'haiku', 1, '2026-10-20T12:00:00Z', { reasoning_tokens: 1 }),
  ]
  const byClient = rollup(rows, (r) => r.client_id)
  check('one rollup per client, in first-seen order', byClient.map((u) => [u.id, u.requests]), [['a', 6], ['b', 2]])
  check('the latest request is kept', byClient[0].last, '2026-10-20T12:00:00Z')
  check('failures and reasoning add up', [byClient[1].errors, byClient[0].reasoning], [1, 1])
  check('tokens are in, out and cache', tokensOf(byClient[0]), 66)
  const byModel = rollup(rows, (r) => r.model).sort(byTokens)
  check('a model\'s rollup is every client on it', byModel[0].rows.map((r) => r.client_id), ['a', 'b', 'a'])
  check('the models add up to the clients', byModel.reduce((n, u) => n + u.requests, 0), 8)
  const merged = withoutAccount(rows.filter((r) => r.client_id === 'a'))
  check('one client\'s rows lose the account', merged.map((r) => [r.slot, r.requests]), [['large', 5], ['small', 1]])
  check('and keep the latest of the two', merged[0].last, '2026-10-20T10:00:00Z')
  check('a share of nothing is nothing', percentOf(3, 0), 0)
}

console.log('\n— every axis covers its data —')
for (const m of [1, 7, 42, 99, 100, 101, 999, 1183402, 5e9, 0.3]) {
  check(`top >= ${m}`, niceTicks(m).at(-1) >= m, true)
}

console.log(failures === 0 ? '\nPASS' : `\nFAIL (${failures})`)
process.exit(failures === 0 ? 0 : 1)
