import { useEffect, useState } from 'react'

type LoadingStateProps = {
  /** What is being waited for, shown beside the spinner. */
  message: string
  /**
   * Milliseconds to wait before showing anything, so that a load that finishes
   * quickly, a cached chunk or a small table, never flashes a spinner. The
   * default shows it at once, which is right for the page's first load.
   */
  delay?: number
  /** `panel` fills the tab like the Stats card does; `inline` is a short row. */
  size?: 'panel' | 'inline'
}

/**
 * A spinner with a line of text, the one loading state every tab shows while
 * something it needs is on its way: the Stats tab's Iceberg metadata on the
 * first load, and the chunks the SPARQL editor, the map, and the graph view
 * are split into.
 */
export function LoadingState({ message, delay = 0, size = 'panel' }: LoadingStateProps) {
  const [visible, setVisible] = useState(delay === 0)

  useEffect(() => {
    if (delay === 0) return
    const timer = setTimeout(() => setVisible(true), delay)
    return () => clearTimeout(timer)
  }, [delay])

  if (!visible) return null
  return (
    <div className={`loading-state ${size}`} role="status" aria-live="polite">
      <span className="spinner" aria-hidden="true" />
      <span className="loading-state-message">{message}</span>
    </div>
  )
}
