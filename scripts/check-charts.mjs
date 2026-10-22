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
check('it has no pages under it', canonical('/settings/configuration'), '/settings')
check('nor any setting of its own', canonical('/settings/images'), '/settings')
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
  const fixed = ['/', '/accounts', '/accounts/x', '/accounts/x/usage', '/clients/x/models', '/activity/models', '/activity/accounts', '/activity/requests', '/settings']
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
  check('activity opens on its overview', hrefs(lvl('/'))[2], '/activity')
  check('activity lists its four pages', hrefs(lvl('/activity/clients')), [
    '/activity/models', '/activity/clients', '/activity/accounts', '/activity/requests',
  ])
  check('and the page open is current', lvl('/activity/clients').current, '/activity/clients')
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
  // Settings is one page on the top level, so the rail stays on the screens.
  check('settings is on the top level', lvl('/settings').back, undefined)
  check('and is current there', lvl('/settings').current, '/settings')

  const trail = (p) => trailOf(canonical(p), name).map((c) => c.label)
  // Every trail starts at the root, and no level is named twice.
  check('the overview is the root alone', trail('/'), ['Overview'])
  check('a list', trail('/clients'), ['Overview', 'Clients'])
  check('the settings page', trail('/settings'), ['Overview', 'Settings'])
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
  const { Binding, BindingMenu, followLabel } = await import(`${OUT}/clients/specs.js`)
  check('a model round-trips', Binding.parse(Binding.model('claude-opus-5-5').value).body, { model: 'claude-opus-5-5' })
  check('a target too', Binding.parse(Binding.follow('claude:opus').value).body, { follows: 'claude:opus' })
  check('a model id with a colon survives', Binding.parse('model:a:b').model, 'a:b')
  check('a slot reads as its binding', Binding.of({ model: '', bound: 'openai:first' }).value, 'follow:openai:first')
  const opus = { follows: 'claude:opus', title: 'Newest Claude Opus' }
  const choices = [
    { model: 'claude-opus-5-5', title: 'Claude Opus 5.5', serving: 'claude-opus-5-5', provider: 'anthropic', followed_by: [opus] },
    { model: 'claude-opus-5', title: 'Claude Opus 5', serving: 'claude-opus-5', provider: 'anthropic' },
    { model: 'gpt-5.5', title: 'GPT-5.5', serving: 'gpt-5.5', provider: 'openai',
      followed_by: [{ follows: 'openai:first', title: 'ChatGPT\'s first-ranked model' }] },
  ]
  const at = (b, slot) => new BindingMenu(choices, b, slot)
  const plain = at(Binding.model('claude-opus-5'))
  check('each model once, by provider, by name with its id as the hint', plain.options.map((o) => [o.group, o.value, o.label, o.hint]), [
    ['Claude', 'model:claude-opus-5-5', 'Claude Opus 5.5', 'claude-opus-5-5'],
    ['Claude', 'model:claude-opus-5', 'Claude Opus 5', 'claude-opus-5'],
    ['ChatGPT', 'model:gpt-5.5', 'GPT-5.5', 'gpt-5.5'],
  ])
  check('no target is an entry of its own', plain.options.some((o) => o.value.startsWith('follow:')), false)
  check('a model no target serves has no checkbox', plain.follow, undefined)

  const following = at(Binding.follow('claude:opus'), { title: 'Newest Claude Opus', serving: 'claude-opus-5-5' })
  check('a slot following shows the model it resolves to, the box checked', [following.value, following.follow?.checked, following.follow?.follows],
    ['model:claude-opus-5-5', true, 'claude:opus'])
  check('and the menu still lists each model once', following.options.length, 3)
  check('unchecking binds the model it serves now', following.setFollow(false).body, { model: 'claude-opus-5-5' })
  const fixed = at(Binding.model('claude-opus-5-5'))
  check('the same model bound as itself: the box offered, unchecked', [fixed.value, fixed.follow?.checked, fixed.follow?.label],
    ['model:claude-opus-5-5', false, 'Follow new Opus releases'])
  check('checking binds the target', fixed.setFollow(true).body, { follows: 'claude:opus' })
  check('picking another model while following follows its target', following.pick('model:gpt-5.5').body, { follows: 'openai:first' })
  check('or binds it as itself when no target serves it', following.pick('model:claude-opus-5').body, { model: 'claude-opus-5' })
  check('picking while not following binds the model', fixed.pick('model:gpt-5.5').body, { model: 'gpt-5.5' })
  check('ChatGPT\'s targets are worded by what they follow', [followLabel('openai:first'), followLabel('openai:light')],
    ["Follow ChatGPT's top model", "Follow ChatGPT's light model"])

  const kept = at(Binding.model('claude-haiku-4-5'), { title: 'claude-haiku-4-5', serving: 'claude-haiku-4-5', unavailable: 'no connected account lists it' })
  check('an unavailable binding stays visible, first, marked and not choosable, with no checkbox',
    [kept.options.length, kept.value, kept.options[0].value, kept.options[0].disabled, kept.options[0].hint, kept.follow],
    [4, 'model:claude-haiku-4-5', 'model:claude-haiku-4-5', true, 'unavailable: no connected account lists it', undefined])
  const lost = at(Binding.follow('claude:sonnet'), { title: 'Newest Claude Sonnet', serving: 'claude:sonnet', unavailable: 'no account lists a Claude sonnet' })
  check('an unresolved target too', [lost.value, lost.options[0].label, lost.options[0].disabled, lost.follow],
    ['follow:claude:sonnet', 'Newest Claude Sonnet', true, undefined])

  check('a new name follows the newest Opus', BindingMenu.initial(choices).body, { follows: 'claude:opus' })
  check('or the first target there is', BindingMenu.initial(choices.slice(1)).body, { follows: 'openai:first' })
  check('or the first model', BindingMenu.initial([choices[1]]).body, { model: 'claude-opus-5' })
  const fresh = at(BindingMenu.initial(choices))
  check('shown as the model it serves, the box checked', [fresh.value, fresh.follow?.checked], ['model:claude-opus-5-5', true])
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
