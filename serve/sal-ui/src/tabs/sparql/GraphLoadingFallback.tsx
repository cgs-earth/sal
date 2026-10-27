import { LoadingState } from '../../components/LoadingState'

/**
 * Suspense fallback shown while GraphView's chunk (sigma/graphology, ~175KB) fetches.
 * Delayed so a fast/cached load — the common case after the first visit — doesn't flash
 * a spinner for a few milliseconds; it only appears once the load is actually taking a while.
 */
export function GraphLoadingFallback() {
  return (
    <div className="graph-loading">
      <LoadingState message="Loading graph view…" delay={200} size="inline" />
    </div>
  )
}
