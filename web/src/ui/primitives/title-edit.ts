/**
 * What pressing Enter in an EditableTitle does with what was typed, apart
 * from the component so `make test-web` can check it without a DOM.
 *
 * The rule is the gateway's (store.CleanName): trimmed, at most MAX_NAME
 * characters, on one line. Checking it here as well is not a second source of
 * truth but a shorter round trip: the server still refuses, and its message is
 * what is shown when it does.
 */

/** store.MaxNameLen: the most characters a client's or an account's name may have. */
export const MAX_NAME = 100

export type TitleCommit =
  /** Nothing to send: the name is what it was. Saving closes the editor. */
  | { kind: 'unchanged' }
  | { kind: 'save'; value: string }
  | { kind: 'invalid'; reason: string }

// Unicode's control characters, as Go's unicode.IsControl reads them: C0, DEL
// and C1. A newline pasted into a name is the usual one.
const CONTROL = /[\u0000-\u001f\u007f-\u009f]/

/**
 * `current` is the stored name ('' for an account known by its address);
 * `clearable` says whether '' is a name worth saving — an account's, which
 * gives it back its address, and not a client's, which must have one.
 */
export function commitTitle(draft: string, current: string, { clearable }: { clearable: boolean }): TitleCommit {
  const value = draft.trim()
  if (value === current) return { kind: 'unchanged' }
  if (value === '' && !clearable) return { kind: 'invalid', reason: 'A name is required.' }
  // Characters, as the server counts them, not UTF-16 units: an emoji is one.
  const length = [...value].length
  if (length > MAX_NAME) {
    return { kind: 'invalid', reason: `At most ${MAX_NAME} characters (this is ${length}).` }
  }
  if (CONTROL.test(value)) return { kind: 'invalid', reason: 'A name has to fit on one line.' }
  return { kind: 'save', value }
}
