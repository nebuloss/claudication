// Prove the public welcome page's bundle says nothing about the gateway.
//
// welcome.html is what anyone who finds a relay's address is served, so every
// byte it loads is public. It must not name a provider, a model, a client, an
// API path or the admin surface: each is a fact about the deployment that the
// person who landed there does not need and a scanner does. The page's own
// copy is written that way, but the bundle is what ships, and it can grow
// such a name without anyone editing the page — one import of a module the
// admin app also uses was once enough to carry the whole admin API client
// into it.
//
// So this reads the build, not the source: it starts at dist/welcome.html,
// follows every asset it references and every asset those reference, and
// fails if any of them matches a forbidden name, or if any of them is also
// part of the admin app's graph. Run after the UI build by `make web` and
// `make web-build`, which is what `make check` and CI run.
//
// Usage: node scripts/check-welcome.mjs [dist directory, default web/dist]

import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

const dist = process.argv[2] ?? 'web/dist'

// Case-insensitive. "claude" does not occur in "claudication", so the
// product's own name passes; the short ones are anchored so a minified
// identifier that happens to contain the letters does not trip them.
//
// internal/httpapi/web keeps the same list for the page served when no UI
// is built; a name added here belongs there too.
const FORBIDDEN = [
  /claude/i,
  /anthropic/i,
  /openai/i,
  /chatgpt/i,
  /(?<![a-z])gpt/i,
  /codex/i,
  /opencode/i,
  /(?<![a-z])crush/i,
  /sonnet/i,
  /(?<![a-z])opus(?![a-z])/i,
  /haiku/i,
  /gemini/i,
  /\/v1(?![0-9])/,
  /\/admin/,
  /oauth/i,
  /x-api-key/i,
  /subscription/i,
  /upstream/i,
  // The admin app's own vocabulary: its tabs and its error type. They name
  // nothing about the deployment, but finding one means an admin module came
  // along, and the next thing it brings may.
  /(?<![a-z])accounts?(?![a-z])/i,
  /(?<![a-z])overview(?![a-z])/i,
  /ApiError/,
]

// An asset reference as Vite writes one into HTML, CSS or JS: an absolute
// /assets/ path, or a relative one between chunks of the same build.
const REF = /(?:\/|\.\/)?assets\/[A-Za-z0-9_.-]+\.(?:js|css|svg|png|woff2?)|\.\/[A-Za-z0-9_.-]+\.(?:js|css)/g

/** Every file the document loads, transitively, as paths under dist. */
function reach(doc) {
  const seen = new Set([doc])
  const queue = [doc]
  while (queue.length > 0) {
    const file = queue.shift()
    const text = readFileSync(join(dist, file), 'utf8')
    for (const m of text.matchAll(REF)) {
      let ref = m[0].replace(/^\.?\//, '')
      // A chunk names its sibling as ./x.js, and they all live in assets/.
      if (!ref.startsWith('assets/')) ref = `assets/${ref}`
      if (seen.has(ref) || !existsSync(join(dist, ref))) continue
      seen.add(ref)
      queue.push(ref)
    }
  }
  return seen
}

if (!existsSync(join(dist, 'welcome.html'))) {
  console.error(`check-welcome: ${dist}/welcome.html is missing — build the UI first`)
  process.exit(1)
}

const welcome = reach('welcome.html')
const admin = existsSync(join(dist, 'index.html')) ? reach('index.html') : new Set()
let failed = false

for (const file of welcome) {
  if (file.startsWith('assets/') && admin.has(file)) {
    console.error(`check-welcome: ${file} is loaded by both the welcome page and the admin app`)
    failed = true
  }
  const text = readFileSync(join(dist, file), 'utf8')
  for (const re of FORBIDDEN) {
    const m = text.match(re)
    if (m === null) continue
    const at = m.index ?? 0
    const context = text.slice(Math.max(0, at - 40), at + 40).replace(/\s+/g, ' ')
    console.error(`check-welcome: ${file} matches ${re}: …${context}…`)
    failed = true
  }
}

if (failed) process.exit(1)
console.log(`check-welcome: ${welcome.size} files, none names the gateway's insides`)
