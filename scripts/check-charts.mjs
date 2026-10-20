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
check('settings is its own page', canonical('/settings'), '/settings')
check('an unknown setting lands on it', canonical('/settings/nonesuch'), '/settings')
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
check('activity opens on requests, its only page', canonical('/activity'), '/activity/requests')
check('chats are not on activity', canonical('/activity/chats'), '/activity/requests')
// Ids are encoded once, whatever they hold.
check('an id is kept encoded', canonical('/clients/a%2Fb'), '/clients/a%2Fb')
check('and decoded when read', segments('/clients/a%2Fb/usage'), ['clients', 'a/b', 'usage'])
check('a broken escape does not throw', segments('/clients/%E0%A4%A'), ['clients', '%E0%A4%A'])
check('a path is built encoded', pathOf('accounts', 'a b', 'usage'), '/accounts/a%20b/usage')

check('a page names its screen', tab('/accounts/8189e5dc/connection'), 'accounts')
check('a client page too', tab('/clients/c1/requests'), 'clients')
// Round trip: every canonical path is its own canonical form.
{
  const fixed = ['/', '/accounts', '/accounts/x', '/accounts/x/usage', '/clients/x/models', '/activity/requests', '/settings', '/settings/configuration']
  check('canonical paths are fixed points', fixed.map((p) => canonical(p)), fixed)
}

// The rail lists only the level a page is on, with the way up; the trail
// lists every level. Names come from the pages, by id.
{
  const name = (kind, id) => `${kind}:${id}`
  const lvl = (p, omit) => navLevel(canonical(p), omit)
  const hrefs = (l) => l.items.map((i) => i.href)

  check('the top level has no way up', lvl('/').back, undefined)
  // The overview is the root itself, reached by the trail's first step.
  check('it lists the screens', lvl('/').items.map((i) => i.label), ['Accounts', 'Clients', 'Activity', 'Settings'])
  check('a list is on the top level', lvl('/clients').current, '/clients')
  check('each screen opens on its first page', hrefs(lvl('/'))[2], '/activity/requests')
  check('an account lists its traffic and its settings', hrefs(lvl('/accounts/a1/usage')), [
    '/accounts/a1/usage', '/accounts/a1/requests',
    '/accounts/a1/models', '/accounts/a1/connection', '/accounts/a1/advanced',
  ])
  check('a client too', hrefs(lvl('/clients/c1/usage')), [
    '/clients/c1/usage', '/clients/c1/chats', '/clients/c1/requests', '/clients/c1/models',
  ])
  // A typed client's names are on its overview; it has no Models page.
  check('a page it has no use for is left out', hrefs(lvl('/clients/c1/usage', ['models'])), [
    '/clients/c1/usage', '/clients/c1/chats', '/clients/c1/requests',
  ])
  check('on its overview none is current', lvl('/accounts/a1').current, '/accounts/a1')
  check('a setting is current like any page', lvl('/clients/c1/models').current, '/clients/c1/models')
  check('and goes back to the list', lvl('/clients/c1/usage').back, { href: '/clients', label: 'Clients' })
  check('settings go back to the top', lvl('/settings/images').back.href, '/')

  const trail = (p) => trailOf(canonical(p), name).map((c) => c.label)
  // Every trail starts at the root, and no level is named twice.
  check('the overview is the root alone', trail('/'), ['Overview'])
  check('a list', trail('/clients'), ['Overview', 'Clients'])
  check('the settings page', trail('/settings'), ['Overview', 'Settings'])
  check('a client is its name, not its name then Overview', trail('/clients/c1'), ['Overview', 'Clients', 'clients:c1'])
  check('a page of an account', trail('/accounts/a1/connection'), ['Overview', 'Accounts', 'accounts:a1', 'Connection'])
  check('a gateway section', trail('/settings/configuration'), ['Overview', 'Settings', 'Configuration'])
  check('an activity page', trail('/activity/requests'), ['Overview', 'Activity', 'Requests'])
  check('the item step is its overview', trailOf('/clients/c1/usage', name)[2].href, '/clients/c1')
}

// A client's files are the committed ones with its own names put in. Checked
// against the real committed files, because the rewrite is only as good as
// its reading of them.
console.log('\n— client configs: the committed files, with the client\'s names —')
{
  const { CRUSH_WINDOW, claudeCodeSh, codexToml, codexCatalog, crushJson, opencodeJsonc, stripComments } =
    await import(`${OUT}/clients/configs.js`)
  const { readFileSync } = await import('node:fs')
  const committed = (name) =>
    readFileSync(new URL(`../configs/clients/${name}`, import.meta.url), 'utf8')

  const effort = (...levels) => ({
    supported: true,
    ...Object.fromEntries(['low', 'medium', 'high', 'xhigh', 'max'].map((l) => [l, { supported: levels.includes(l) }])),
  })
  // A slot's list entry: the name, carrying the facts of the model it serves.
  const opus = { id: 'x', display_name: 'Claude Opus 5.5', virtual: true, serving: 'claude-opus-5-5',
    max_input_tokens: 1000000, max_tokens: 128000,
    capabilities: { effort: effort('low', 'medium', 'high', 'xhigh', 'max'), image_input: { supported: true } } }
  const sonnet = { id: 'x', display_name: 'Claude Sonnet 4.5', virtual: true, serving: 'claude-sonnet-4-5-20250929',
    max_input_tokens: 200000, max_tokens: 64000,
    capabilities: { effort: { supported: false }, image_input: { supported: true } } }
  const gpt = { id: 'x', display_name: 'GPT-5.5', virtual: true, serving: 'gpt-5.5', provider: 'openai',
    max_input_tokens: 400000, max_tokens: 128000, capabilities: { effort: effort('low', 'medium', 'high') } }

  // crush: the two slots, on either API.
  const crushSlots = [
    { id: 'large', role: 'main', field: 'models.large', name: 'large', served: opus },
    { id: 'small', role: 'fast', field: 'models.small', name: 'small', served: sonnet },
  ]
  const crush = JSON.parse(crushJson(committed('crush.json'), { dialect: 'anthropic', slots: crushSlots }, 'https://gw'))
  const provider = Object.values(crush.providers)[0]
  const byID = Object.fromEntries(provider.models.map((m) => [m.id, m]))
  check('crush lists the client\'s names', provider.models.map((m) => m.id), ['large', 'small'])
  check('a 1M model is declared at the compaction window', byID.large.context_window, CRUSH_WINDOW)
  check('a 200k model keeps its own window', byID.small.context_window, 200000)
  check('a model with no effort setting does not reason', [byID.small.can_reason, 'reasoning_levels' in byID.small], [false, false])
  check('the output cap is the served model\'s', byID.large.default_max_tokens, 128000)
  check('on Messages, the bare address and thinking on', [provider.type, provider.base_url, crush.models.large.think], ['anthropic', 'https://gw', true])
  check('the rest of crush.json is the committed file\'s', [crush.$schema, Object.keys(crush.providers)[0]],
    [JSON.parse(committed('crush.json')).$schema, 'claudication'])
  // The committed file is what a new crush client is handed, so writing it
  // with the default names and the committed facts changes nothing.
  const fresh = crushJson(committed('crush.json'), { dialect: 'anthropic', slots: crushSlots.map((s) => ({ ...s, served: opus })) }, 'https://claudication.example.com')
  check('the committed file is a new client\'s, byte for byte', fresh, committed('crush.json'))
  const crushR = JSON.parse(crushJson(committed('crush.json'), {
    dialect: 'openai', slots: [{ ...crushSlots[0], name: 'gpt-5-large', served: gpt }, { ...crushSlots[1], name: 'gpt-5-small', served: gpt }],
  }, 'https://gw'))
  const pr = Object.values(crushR.providers)[0]
  check('on Responses, the openai type at /v1 and no think', [pr.type, pr.base_url, 'think' in crushR.models.large], ['openai', 'https://gw/v1', false])
  check('and the names asked for', [crushR.models.large.model, crushR.models.small.model], ['gpt-5-large', 'gpt-5-small'])

  // Codex: four slots, one of them in two fields.
  const codexSlots = [
    { id: 'large', role: 'main', field: 'model', name: 'big', served: opus },
    { id: 'review', role: 'review', field: 'review_model', name: 'rev', served: gpt },
    { id: 'agents', role: 'subagent', field: 'agents.default_subagent_model', name: 'sub', served: sonnet },
    { id: 'small', role: 'memories', field: 'memories.extract_model, memories.consolidation_model', name: 'mem', served: sonnet },
  ]
  const toml = codexToml(committed('codex.toml'), { dialect: 'openai', slots: codexSlots })
  check('codex.toml names every slot', [
    /^model = "big"$/m.test(toml), /^review_model = "rev"$/m.test(toml), /^default_subagent_model = "sub"$/m.test(toml),
    /^extract_model = "mem"$/m.test(toml), /^consolidation_model = "mem"$/m.test(toml),
  ], [true, true, true, true, true])
  check('and nothing else in it moves', toml.split('\n').length, committed('codex.toml').split('\n').length)
  const codex = JSON.parse(codexCatalog(committed('codex-models.json'), { dialect: 'openai', slots: codexSlots })).models
  const template = JSON.parse(committed('codex-models.json')).models[0]
  check('the catalog has one entry per name, in order', codex.map((m) => [m.slug, m.priority]),
    [['big', 10], ['rev', 20], ['sub', 30], ['mem', 40]])
  check('each at its served model\'s window', codex.map((m) => m.context_window), [1000000, 400000, 200000, 200000])
  check('each defers its reviews to the review name', codex.map((m) => m.auto_review_model_override), ['rev', 'rev', 'rev', 'rev'])
  check('everything else in an entry is the committed one', codex[2].base_instructions, template.base_instructions)

  // Claude Code: every model variable its slot's name, the picker told what each behaves like.
  const ccSlots = [
    ['ANTHROPIC_MODEL', 'main', 'claude-large', opus],
    ['ANTHROPIC_SMALL_FAST_MODEL', 'fast', 'claude-small', sonnet],
    ['ANTHROPIC_DEFAULT_OPUS_MODEL', 'opus', 'claude-opus', opus],
    ['ANTHROPIC_DEFAULT_SONNET_MODEL', 'sonnet', 'claude-sonnet-mine', sonnet],
    ['CLAUDE_CODE_AUTO_MODE_MODEL', 'classifier', 'claude-auto', sonnet],
  ].map(([field, role, name, served]) => ({ id: name, role, field, name, served }))
  const sh = claudeCodeSh(committed('claude-code.sh'), { dialect: 'anthropic', slots: ccSlots })
  check('each variable names its slot', [
    /^export ANTHROPIC_MODEL=claude-large$/m.test(sh), /^export ANTHROPIC_DEFAULT_SONNET_MODEL=claude-sonnet-mine$/m.test(sh),
    /^export CLAUDE_CODE_AUTO_MODE_MODEL=claude-auto$/m.test(sh),
  ], [true, true, true])
  check('a variable with no slot is left out', /ANTHROPIC_DEFAULT_FABLE_MODEL/.test(sh), false)
  const picker = JSON.parse(sh.match(/--settings '(\{.*\})'/)[1]).modelPicker.options
  check('the picker says what each name behaves like', picker.map((o) => `${o.model}=${o.behavesAs}`), [
    'claude-large=claude-opus-5-5', 'claude-small=claude-sonnet-4-5-20250929', 'claude-opus=claude-opus-5-5',
    'claude-sonnet-mine=claude-sonnet-4-5-20250929',
  ])
  check('and leaves the classifier out: nobody picks it', picker.some((o) => o.model === 'claude-auto'), false)

  // opencode: its built-in provider of the client's API, overridden.
  const ocSlots = [
    { id: 'large', role: 'main', field: 'model', name: 'large', served: opus },
    { id: 'small', role: 'fast', field: 'small_model', name: 'small', served: sonnet },
  ]
  const ocR = opencodeJsonc(committed('opencode.jsonc'), { dialect: 'openai', slots: ocSlots })
  const ocRj = JSON.parse(stripComments(ocR))
  check('on Responses, the openai provider', [Object.keys(ocRj.provider), ocRj.model, ocRj.small_model], [['openai'], 'openai/large', 'openai/small'])
  check('each name at its model\'s limits', ocRj.provider.openai.models.small.limit, { context: 200000, output: 64000 })
  check('with no thinking modes on Responses', 'variants' in ocRj.provider.openai.models.large, false)
  check('its opening comment kept', ocR.includes('// opencode, pointed at a claudication gateway.'), true)
  check('a URL\'s // is not a comment', stripComments('{"u": "https://x"} // c'), '{"u": "https://x"} \n')
  const ocM = JSON.parse(stripComments(opencodeJsonc(committed('opencode.jsonc'), {
    dialect: 'anthropic', slots: ocSlots.map((s) => ({ ...s, name: `claude-${s.name}` })),
  })))
  check('on Messages, the anthropic provider and claude names', [Object.keys(ocM.provider), ocM.model], [['anthropic'], 'anthropic/claude-large'])
  check('with adaptive thinking for a reasoning model', Object.keys(ocM.provider.anthropic.models['claude-large'].variants), ['low', 'medium', 'high', 'xhigh', 'max'])
  check('and the committed baseURL', ocM.provider.anthropic.options, JSON.parse(stripComments(committed('opencode.jsonc'))).provider.openai.options)
}

console.log('\n— client specs: a slot\'s binding is one menu value —')
{
  const { Binding, bindingOptions } = await import(`${OUT}/clients/specs.js`)
  check('a model round-trips', Binding.parse(Binding.model('claude-opus-5-5').value).body, { model: 'claude-opus-5-5' })
  check('a target too', Binding.parse(Binding.follow('claude:opus').value).body, { follows: 'claude:opus' })
  check('a model id with a colon survives', Binding.parse('model:a:b').model, 'a:b')
  check('a slot reads as its binding', Binding.of({ model: '', bound: 'openai:first' }).value, 'follow:openai:first')
  const choices = [
    { follows: 'claude:opus', title: 'Newest Claude Opus', serving: 'claude-opus-5-5', provider: 'anthropic' },
    { model: 'claude-opus-5-5', title: 'Claude Opus 5.5', serving: 'claude-opus-5-5', provider: 'anthropic' },
    { model: 'gpt-5.5', title: 'GPT-5.5', serving: 'gpt-5.5', provider: 'openai' },
  ]
  const opts = bindingOptions(choices)
  check('the server\'s choices, targets first, then each provider\'s models', opts.map((o) => [o.group, o.value]), [
    ['Follow the newest', 'follow:claude:opus'], ['Claude', 'model:claude-opus-5-5'], ['ChatGPT', 'model:gpt-5.5'],
  ])
  check('a target says what it serves now', opts[0].hint, 'now claude-opus-5-5')
  check('a current binding that is a choice adds nothing', bindingOptions(choices, { model: 'gpt-5.5', title: 'GPT-5.5' }).length, 3)
  const kept = bindingOptions(choices, { model: 'claude-haiku-4-5', title: 'claude-haiku-4-5', unavailable: 'no connected account lists it' })
  check('an unavailable binding stays visible, first, marked and not choosable', [kept.length, kept[0].value, kept[0].disabled, kept[0].hint],
    [4, 'model:claude-haiku-4-5', true, 'unavailable: no connected account lists it'])
}

console.log('\n— model tests: a few at a time, each result as it lands —')
{
  const { eachBounded, explain, failing } = await import(`${OUT}/domain/model-tests.js`)
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

console.log('\n— every axis covers its data —')
for (const m of [1, 7, 42, 99, 100, 101, 999, 1183402, 5e9, 0.3]) {
  check(`top >= ${m}`, niceTicks(m).at(-1) >= m, true)
}

console.log(failures === 0 ? '\nPASS' : `\nFAIL (${failures})`)
process.exit(failures === 0 ? 0 : 1)
