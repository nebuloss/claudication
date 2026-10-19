// merge.py for a machine with node and no python3; the same rules, kept in
// step by the tests that run both.
//   node merge.js TARGET NEW OWNED...
const fs = require('fs')

function strip(s) {
  let out = ''
  let i = 0
  const n = s.length
  while (i < n) {
    const c = s[i]
    if (c === '"') {
      let j = i + 1
      while (j < n && s[j] !== '"') j += s[j] === '\\' ? 2 : 1
      out += s.slice(i, j + 1)
      i = j + 1
    } else if (s.startsWith('//', i)) {
      const j = s.indexOf('\n', i)
      i = j < 0 ? n : j
    } else if (s.startsWith('/*', i)) {
      const j = s.indexOf('*/', i + 2)
      i = j < 0 ? n : j + 2
    } else if (c === ',') {
      let j = i + 1
      while (j < n && ' \t\r\n'.includes(s[j])) j++
      if (j >= n || !'}]'.includes(s[j])) out += c
      i++
    } else {
      out += c
      i++
    }
  }
  return out
}

function main() {
  const [target, next, ...owned] = process.argv.slice(2)
  let cur
  try {
    const text = strip(fs.readFileSync(target, 'utf8')).trim()
    cur = text === '' ? {} : JSON.parse(text)
  } catch {
    return 3
  }
  if (cur === null || typeof cur !== 'object' || Array.isArray(cur)) return 3
  const want = JSON.parse(strip(fs.readFileSync(next, 'utf8')))

  for (const path of owned) {
    const keys = path.split('.')
    let v = want
    for (const k of keys) v = v !== null && typeof v === 'object' ? v[k] : undefined
    if (v === undefined) continue
    let d = cur
    for (const k of keys.slice(0, -1)) {
      if (d[k] === null || typeof d[k] !== 'object' || Array.isArray(d[k])) d[k] = {}
      d = d[k]
    }
    d[keys[keys.length - 1]] = v
  }
  for (const [k, v] of Object.entries(want)) if (!(k in cur)) cur[k] = v

  fs.writeFileSync(target, JSON.stringify(cur, null, 2) + '\n')
  return 0
}

process.exit(main())
