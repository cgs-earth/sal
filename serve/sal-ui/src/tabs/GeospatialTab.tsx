import { useEffect, useState } from 'react'
import type { QueryResult } from '../api'
import type { MapView as MapViewName, OgcRequest } from '../routing'
import type { ResultSource } from '../results'
import { MapView } from './map/MapView'
import { FeatureView } from './map/FeatureView'

/** The view shown last, so leaving the tab and coming back lands on the same one. */
const persisted: { view: MapViewName | null } = { view: null }

const VIEWS: { name: MapViewName; label: string; title: string }[] = [
  {
    name: 'map',
    label: 'Map view',
    title: 'Everything inside a box, or what the last query returned',
  },
  {
    name: 'features',
    label: 'Feature view',
    title: 'Individual features through the OGC API Features endpoint',
  },
]

type GeospatialTabProps = {
  /** The view the URL asked for, if it named one. */
  view: MapViewName | null
  /** The OGC request the URL named, for the feature view to run on arrival, or null. */
  request: OgcRequest | null
  /** The editor's result the URL named, for the map view to draw on arrival, or null. */
  source: ResultSource | null
  /** The navigation count, so that a view asked for again is switched to again. */
  arrival: number
  /** The Stats tab's snapshot listing, for the feature view's snapshot choice. */
  snapshots: QueryResult | null
}

/**
 * The Geospatial tab, which is two views under one header: the map view draws the
 * table's geometries, and the feature view looks at individual features
 * through the OGC API Features endpoint. The strip in the header switches
 * between them.
 */
export function GeospatialTab({ view: requested, request, source, arrival, snapshots }: GeospatialTabProps) {
  const [view, setView] = useState<MapViewName>(requested ?? persisted.view ?? 'map')

  useEffect(() => {
    if (requested) setView(requested)
  }, [requested, arrival])

  useEffect(() => {
    persisted.view = view
  }, [view])

  const subtabs = (
    <div className="subtabs" role="tablist" aria-label="Geospatial views">
      {VIEWS.map((candidate) => (
        <button
          key={candidate.name}
          type="button"
          role="tab"
          aria-selected={view === candidate.name}
          className={view === candidate.name ? 'subtab active' : 'subtab'}
          title={candidate.title}
          onClick={() => setView(candidate.name)}
        >
          {candidate.label}
        </button>
      ))}
    </div>
  )

  return (
    <div className="tab-body map">
      <section className="panel map-panel">
        {view === 'map' ? <MapView subtabs={subtabs} requested={source} arrival={arrival} /> : <FeatureView subtabs={subtabs} snapshots={snapshots} request={request} />}
      </section>
    </div>
  )
}
