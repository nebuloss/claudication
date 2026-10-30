import type { ReactNode } from 'react'
import { ICON } from './icons'

/** Where a step stands: done, being done now, or still to come. */
export type StepState = 'done' | 'active' | 'todo'

export type Step = {
  title: ReactNode
  /** A line under the title: what to do, or what happens. */
  detail?: ReactNode
  state: StepState
  /** What the step is done with — a field, a code, a button — shown while it is the one being done. */
  children?: ReactNode
}

/**
 * A task in steps, top to bottom: a numbered disc for each, joined by a line,
 * a tick once it is done and the primary colour while it is the one being
 * done, which alone shows what it is done with. The sign-in flows are this,
 * so whichever provider it is, the operator sees where they are in it.
 */
export function Steps({ steps }: { steps: Step[] }) {
  return (
    <ol className="m-0 flex list-none flex-col p-0">
      {steps.map((s, i) => {
        const last = i === steps.length - 1
        return (
          <li key={i} className="relative flex gap-3" aria-current={s.state === 'active' ? 'step' : undefined}>
            {/* The line down to the next disc, behind this one. */}
            {!last && (
              <span
                aria-hidden
                className={`absolute top-8 bottom-0 left-[15px] w-0.5 ${
                  s.state === 'done' ? 'bg-primary' : 'bg-outline-variant'
                }`}
              />
            )}
            <span
              className={`relative z-10 grid size-8 shrink-0 place-items-center rounded-full text-sm font-medium ${
                s.state === 'done'
                  ? 'bg-primary text-on-primary'
                  : s.state === 'active'
                    ? 'bg-primary-container text-on-primary-container ring-2 ring-primary'
                    : 'bg-surface-lowest text-on-surface-variant ring-1 ring-outline-variant'
              }`}
            >
              {s.state === 'done' ? (
                <svg viewBox="0 0 24 24" aria-hidden className="size-4 fill-current">
                  <path d={ICON.done} />
                </svg>
              ) : (
                i + 1
              )}
            </span>
            <div className={`min-w-0 flex-1 ${last ? '' : 'pb-5'}`}>
              <div
                className={`pt-1 text-sm font-medium ${s.state === 'todo' ? 'text-on-surface-variant' : 'text-on-surface'}`}
              >
                {s.title}
              </div>
              {s.detail !== undefined && <div className="mt-0.5 text-xs text-on-surface-variant">{s.detail}</div>}
              {s.state === 'active' && s.children !== undefined && <div className="mt-3">{s.children}</div>}
            </div>
          </li>
        )
      })}
    </ol>
  )
}
