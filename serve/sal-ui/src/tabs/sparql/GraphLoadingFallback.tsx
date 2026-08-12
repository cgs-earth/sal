import { useEffect, useState } from 'react'

/**
 * Suspense fallback shown while GraphView's chunk (sigma/graphology, ~175KB) fetches.
 * Delayed so a fast/cached load — the common case after the first visit — doesn't flash
 * a spinner for a few milliseconds; it only appears once the load is actually taking a while.
 */
export function GraphLoadingFallback() {
  const [visible, setVisible] = useState(false)

  useEffect(() => {
    const timer = setTimeout(() => setVisible(true), 200)
    return () => clearTimeout(timer)
  }, [])

  if (!visible) return null
  return (
    <div className="graph-loading">
      <span className="graph-spinner" aria-hidden="true" />
      Loading graph view…
    </div>
  )
}
