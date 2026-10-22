/**
 * What a checklist tree works out from the data it is given, apart from the
 * component so `make test-web` can check it without a DOM: how deep each node
 * is, what a group's box shows, how many of its leaves are in, which nodes a
 * search leaves standing and which of them are open.
 *
 * The caller hands over nodes and nothing else — no levels, no counts, no
 * group states — because each of those is a fact about the shape, and a
 * caller that works one out by hand works it out once per list and wrong in
 * one of them. This used to be that: the models lists passed a level per row
 * and a shift for items, and only the depth they were written for lined up.
 */

/** What the tree reads off a node. The component's node adds what it draws. */
export interface TreeNode {
  /** Unique in the tree: what onChange reports and a fold is remembered by. */
  key: string
  /** What the node is called; a search matches it. */
  label: string
  /** More words a search matches besides the label: a model's id. */
  search?: string
  /** A leaf's box. Ignored on a group, whose box is its leaves'. */
  checked?: boolean
  /** A group's nodes. Present — even empty — is what makes a node a group. */
  children?: readonly TreeNode[]
  /** Open before anyone has folded it. */
  defaultOpen?: boolean
  disabled?: boolean
}

export type BoxState = 'on' | 'off' | 'mixed'

/** A node as it is drawn: where it sits and what its box says. */
export interface Placed<N extends TreeNode> {
  node: N
  /** 0 at the top, one more per group it is inside. */
  depth: number
  /** A group; false for a leaf. */
  group: boolean
  /** Shown open: unfolded, or the search is holding it open. */
  open: boolean
  /**
   * The leaves under it that a box can move, the node itself for a leaf:
   * what its box reports. Under a search, only the ones the search shows —
   * "find the Haikus, uncheck their family" leaves the Sonnets alone.
   */
  leaves: string[]
  state: BoxState
  /** In of what there is, under the search's cut: "3 of 4". */
  count: string
  /** Every leaf disabled, or the node itself. */
  disabled: boolean
  /** Drawn only while open. */
  children: Placed<N>[]
}

/** On when every one is, off when none is, mixed between — and off when there are none. */
export function boxState(on: readonly boolean[]): BoxState {
  const n = on.filter(Boolean).length
  return n === 0 ? 'off' : n === on.length ? 'on' : 'mixed'
}

/** Each word of the query, in the label or the search words, any order, any case. */
export function matcher(query: string): ((n: TreeNode) => boolean) | undefined {
  const words = query.toLowerCase().split(/\s+/).filter((w) => w !== '')
  if (words.length === 0) return undefined
  return (n) => {
    const text = `${n.label} ${n.search ?? ''}`.toLowerCase()
    return words.every((w) => text.includes(w))
  }
}

/**
 * The nodes a search leaves: every leaf it matches and the groups above one,
 * the rest gone. Groups are not matched on their own label — a family whose
 * name matches still shows only the models that do, so the box over it means
 * the rows under it. Without a query, everything.
 */
export function cut<N extends TreeNode>(nodes: readonly N[], query: string): N[] {
  const hit = matcher(query)
  if (hit === undefined) return [...nodes]
  const keep = (list: readonly TreeNode[]): TreeNode[] =>
    list.flatMap((n) => {
      if (n.children === undefined) return hit(n) ? [n] : []
      const children = keep(n.children)
      return children.length > 0 ? [{ ...n, children }] : []
    })
  return keep(nodes) as N[]
}

/** The keys of every leaf under these nodes, in order. */
export function leafKeys(nodes: readonly TreeNode[]): string[] {
  return nodes.flatMap((n) => (n.children === undefined ? [n.key] : leafKeys(n.children)))
}

/** The groups open before anyone folds one: the ones that say so. */
export function defaultOpen(nodes: readonly TreeNode[]): Set<string> {
  const open = new Set<string>()
  const walk = (list: readonly TreeNode[]) => {
    for (const n of list) {
      if (n.children === undefined) continue
      if (n.defaultOpen === true) open.add(n.key)
      walk(n.children)
    }
  }
  walk(nodes)
  return open
}

/**
 * The tree as it is drawn: the search's cut, each node placed at its depth
 * with its box worked out from its leaves. While searching every group is
 * open, because a match folded away is a match not found; otherwise
 * `isOpen` says.
 */
export function place<N extends TreeNode>(
  nodes: readonly N[],
  isOpen: (key: string) => boolean,
  query = '',
): Placed<N>[] {
  const searching = matcher(query) !== undefined
  const visit = (n: N, depth: number): Placed<N> => {
    if (n.children === undefined) {
      const on = n.checked === true
      return {
        node: n,
        depth,
        group: false,
        open: false,
        leaves: [n.key],
        state: on ? 'on' : 'off',
        count: on ? '1 of 1' : '0 of 1',
        disabled: n.disabled === true,
        children: [],
      }
    }
    const children = (n.children as readonly N[]).map((c) => visit(c, depth + 1))
    // The leaves below, each once, for the box and the count; a disabled one
    // is counted (it is in or out all the same) but not moved by the box.
    const below: { key: string; on: boolean; disabled: boolean }[] = []
    const collect = (list: readonly TreeNode[], off: boolean) => {
      for (const c of list) {
        const d = off || c.disabled === true
        if (c.children === undefined) below.push({ key: c.key, on: c.checked === true, disabled: d })
        else collect(c.children, d)
      }
    }
    collect(n.children, n.disabled === true)
    const movable = below.filter((l) => !l.disabled)
    return {
      node: n,
      depth,
      group: true,
      open: searching || isOpen(n.key),
      leaves: movable.map((l) => l.key),
      state: boxState(below.map((l) => l.on)),
      count: `${below.filter((l) => l.on).length} of ${below.length}`,
      disabled: movable.length === 0,
      children,
    }
  }
  return cut(nodes, query).map((n) => visit(n, 0))
}

/** How many levels the deepest leaf sits under the top: 0 for a flat list. */
export function leafDepth(nodes: readonly TreeNode[]): number {
  let deepest = 0
  const walk = (list: readonly TreeNode[], depth: number) => {
    for (const n of list) {
      if (n.children === undefined) deepest = Math.max(deepest, depth)
      else walk(n.children, depth + 1)
    }
  }
  walk(nodes, 0)
  return deepest
}

/**
 * The leaves a search shows, in order: what a preset beside the tree acts on
 * while there is one, so "find the Haikus, None" is two steps.
 */
export function shownLeaves(nodes: readonly TreeNode[], query: string): string[] {
  return leafKeys(cut(nodes, query))
}
