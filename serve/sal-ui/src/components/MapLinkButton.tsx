import { featuresFromResult } from '../geo'
import type { ResultSource } from '../results'
import { mapLink, visit } from '../routing'

type MapLinkButtonProps = {
  /** Which editor's result the map should draw. */
  source: ResultSource
  /** The result as the editor holds it; the button shows only when a cell of it is WKT or GeoJSON. */
  header: string[] | null
  rows: string[][] | null
  className?: string
  /** The tooltip, when the default does not fit. */
  title?: string
  /** Run before the map view is opened, such as publishing the result it should draw. */
  onBeforeVisit?: () => void
}

/**
 * An icon button on a result that can be drawn: it opens the Geospatial tab's
 * map view on that result, the same drawing the map view's own chips
 * select. It renders nothing when no cell of the result holds a geometry,
 * so a result with nothing to draw offers nothing.
 */
export function MapLinkButton({ source, header, rows, className = '', title, onBeforeVisit }: MapLinkButtonProps) {
  if (!header || !rows) return null
  const { features } = featuresFromResult(header, rows)
  if (features.length === 0) return null
  const label = title ?? `Draw the ${features.length} ${features.length === 1 ? 'geometry' : 'geometries'} of this result on the map`
  return (
    <button
      type="button"
      className={`icon-button map-link-button ${className}`.trim()}
      title={label}
      aria-label={label}
      onClick={() => {
        onBeforeVisit?.()
        visit(mapLink(source))
      }}
    >
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <path d="M3 6.5l6-2.5 6 2.5 6-2.5v14l-6 2.5-6-2.5-6 2.5z" />
        <path d="M9 4v14M15 6.5v14" />
      </svg>
    </button>
  )
}
