import type { ReactNode } from 'react'

/**
 * HeaderTip wraps a table-header label and shows a themed definition tooltip on
 * hover (CSS-only, via the `.th-tip` styles). The enclosing <th>'s click-to-sort
 * behavior is preserved — this only wraps the label text.
 *
 * `align` controls which edge the tooltip box anchors to, so trailing columns
 * don't overflow the horizontally-scrollable table container.
 */
export default function HeaderTip({
  tip,
  align = 'left',
  children,
}: {
  tip: string
  align?: 'left' | 'right'
  children: ReactNode
}) {
  return (
    <span className={`th-tip th-tip-${align}`} data-tip={tip}>
      {children}
    </span>
  )
}
