import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type maplibregl from 'maplibre-gl'
import type { GeoJSONSource, LngLat } from 'maplibre-gl'
import type { Feature, FeatureCollection } from 'geojson'
import { fetchExtent, fetchGeometries, type BBox, type GeoJSONFeature } from '../../api'
import { boundsOf, boxPolygon, featuresFromResult, type MapFeature } from '../../geo'
import { useResults, type ResultSource } from '../../results'
import { pathForTab, visit } from '../../routing'
import { addExtentLayers } from '../../basemap'
import type { MapPalette } from '../../theme'
import { propertiesTable, useMapLibre } from '../../components/useMapLibre'

/**
 * What the map draws: the last result of one of the editors, or a bounding box
 * query of its own. The box is not offered as a choice; querying one selects it.
 */
type MapSource = ResultSource | 'bbox'

/** Four coordinates as typed, so that a half-typed number is not rejected mid-keystroke. */
type BBoxInputs = [string, string, string, string]

/*
 * The settings survive leaving the tab. The map itself is torn down whenever
 * another tab is shown, but the box and the toggles the user had set up
 * should still be there on return, so they live outside the component.
 */
const persisted: {
  source: MapSource | null
  inputs: BBoxInputs | null
  showExtent: boolean
  /** The arrival whose source the map last selected, so returning to the view does not reselect it. */
  served: number
} = { source: null, inputs: null, showExtent: false, served: 0 }

/** How long a box query runs before the header says it is still querying. */
const SLOW_QUERY_MS = 600

/** The padding, in pixels, fitBounds keeps around whatever it fits. */
const FIT_PADDING = 48

const EMPTY: FeatureCollection = { type: 'FeatureCollection', features: [] }

/** How each drawable result is named in the chips and the line under them. */
const SOURCE_LABELS: Record<ResultSource, string> = {
  SQL: 'SQL result',
  SPARQL: 'SPARQL result',
  Features: 'feature view result',
}

/** The layers a click on a feature is looked up in, topmost first. */
const FEATURE_LAYERS = ['features-point', 'features-line', 'features-fill']

/**
 * Adds the sources and layers the tab draws into. MapLibre paint wants literal
 * colors, not CSS variables, so they come from the theme's palette; a theme
 * switch loads the other basemap, which drops these, and they are added again
 * in the other palette.
 */
function addLayers(map: maplibregl.Map, { feature, extent, point, pointStroke, box }: MapPalette) {
  addExtentLayers(map, extent)
  map.addSource('bbox', { type: 'geojson', data: EMPTY })
  map.addSource('features', { type: 'geojson', data: EMPTY })
  map.addLayer({
    id: 'features-fill',
    type: 'fill',
    source: 'features',
    filter: ['match', ['geometry-type'], ['Polygon', 'MultiPolygon'], true, false],
    paint: { 'fill-color': feature, 'fill-opacity': 0.22 },
  })
  map.addLayer({
    id: 'features-line',
    type: 'line',
    source: 'features',
    filter: ['match', ['geometry-type'], ['Polygon', 'MultiPolygon', 'LineString', 'MultiLineString'], true, false],
    paint: { 'line-color': feature, 'line-width': 2 },
  })
  map.addLayer({
    id: 'features-point',
    type: 'circle',
    source: 'features',
    filter: ['match', ['geometry-type'], ['Point', 'MultiPoint'], true, false],
    paint: {
      'circle-radius': 6,
      'circle-color': point,
      'circle-stroke-color': pointStroke,
      'circle-stroke-width': 1.5,
    },
  })
  map.addLayer({
    id: 'bbox-line',
    type: 'line',
    source: 'bbox',
    paint: { 'line-color': box, 'line-width': 2, 'line-dasharray': [2, 2] },
  })
}

function setData(map: maplibregl.Map, source: string, data: FeatureCollection | Feature) {
  const geojson = map.getSource<GeoJSONSource>(source)
  geojson?.setData(data)
}

/**
 * The box a click queries: centered on the point, a square on screen, and sized
 * so that fitting it afterwards with FIT_PADDING leaves the zoom where it is.
 * It is measured in pixels and unprojected rather than drawn in degrees, since
 * a square in degrees is neither square on screen nor the same size at every
 * zoom, and a box sized from the previous one grew past the viewport whenever
 * that one was wider than it was tall.
 */
function viewportBox(map: maplibregl.Map, point: LngLat): BBox {
  const { clientWidth, clientHeight } = map.getContainer()
  const side = Math.max(Math.min(clientWidth, clientHeight) - 2 * FIT_PADDING, 32)
  const center = map.project(point)
  const southWest = map.unproject([center.x - side / 2, center.y + side / 2])
  const northEast = map.unproject([center.x + side / 2, center.y - side / 2])
  return [southWest.lng, Math.max(-90, southWest.lat), northEast.lng, Math.min(90, northEast.lat)]
}

function parseInputs(inputs: BBoxInputs): BBox | null {
  const numbers = inputs.map((value) => Number.parseFloat(value.trim()))
  if (numbers.some((n) => !Number.isFinite(n))) return null
  const [w, s, e, n] = numbers as BBox
  if (w > e || s > n) return null
  return [w, s, e, n]
}

function formatInputs(box: BBox): BBoxInputs {
  return box.map((n) => String(Number(n.toFixed(6)))) as BBoxInputs
}

/** The GeoSPARQL form of a box query, for carrying the box over to the SPARQL editor. */
function sparqlBoxQuery(box: BBox): string {
  return `PREFIX geo: <http://www.opengis.net/ont/geosparql#>
PREFIX geof: <http://www.opengis.net/def/function/geosparql/>

SELECT ?feature ?geometry ?wkt
WHERE {
  ?feature geo:hasGeometry ?geometry .
  ?geometry geo:asWKT ?wkt .
  FILTER(geof:sfIntersects(?wkt, "POLYGON((${box[0]} ${box[1]}, ${box[2]} ${box[1]}, ${box[2]} ${box[3]}, ${box[0]} ${box[3]}, ${box[0]} ${box[1]}))"^^geo:wktLiteral))
}`
}

/**
 * The map view of the Geospatial tab: everything the table holds inside a box, or
 * what the last query returned. The feature view beside it is where one
 * feature is looked at; subtabs is the strip that switches between the two.
 */
export function MapView({ subtabs, requested, arrival }: { subtabs: ReactNode; requested: ResultSource | null; arrival: number }) {
  const results = useResults()
  const container = useRef<HTMLDivElement>(null)

  const [source, setSource] = useState<MapSource>(() => persisted.source ?? results.latest ?? 'bbox')
  const [inputs, setInputs] = useState<BBoxInputs>(() => persisted.inputs ?? ['', '', '', ''])
  const [showExtent, setShowExtent] = useState(persisted.showExtent)
  const [extent, setExtent] = useState<GeoJSONFeature | null>(null)
  const [boxResult, setBoxResult] = useState<{ box: BBox; features: MapFeature[]; seconds: number } | null>(null)
  const [loading, setLoading] = useState(false)
  // The Querying badge is for a query that is taking a while, not a flash on
  // every click: it shows once a query has run for this long.
  const [slow, setSlow] = useState(false)
  useEffect(() => {
    if (!loading) {
      setSlow(false)
      return
    }
    const timer = setTimeout(() => setSlow(true), SLOW_QUERY_MS)
    return () => clearTimeout(timer)
  }, [loading])
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    persisted.source = source
    persisted.inputs = inputs
    persisted.showExtent = showExtent
  }, [source, inputs, showExtent])

  const box = useMemo(() => parseInputs(inputs), [inputs])

  // While the map draws a result, the box controls are grayed out and a click
  // on the map does nothing, so the result is not replaced by accident. The
  // map's click handler is registered once, so it reads the lock through a ref.
  const locked = source !== 'bbox'
  const boxLocked = useRef(locked)
  boxLocked.current = locked

  // A map button opens the view on its result, each time it is pressed.
  useEffect(() => {
    if (!requested || arrival === persisted.served) return
    persisted.served = arrival
    setSource(requested)
  }, [requested, arrival])

  // Each editor's result is converted once per result rather than per render,
  // since the chips show the counts of both whichever one is drawn.
  const converted = useMemo(
    () => ({
      SQL: results.SQL && featuresFromResult(results.SQL.header, results.SQL.rows),
      SPARQL: results.SPARQL && featuresFromResult(results.SPARQL.header, results.SPARQL.rows),
      Features: results.Features && featuresFromResult(results.Features.header, results.Features.rows),
    }),
    [results],
  )

  // Runs the bounding box query for a box and shows its features.
  const queryBox = useCallback(async (target: BBox) => {
    setSource('bbox')
    setLoading(true)
    setError(null)
    const started = performance.now()
    try {
      const collection = await fetchGeometries({ bbox: target, limit: 1000 })
      setBoxResult({
        box: target,
        features: collection.features.filter((f) => f.geometry) as MapFeature[],
        seconds: (performance.now() - started) / 1000,
      })
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught))
    } finally {
      setLoading(false)
    }
  }, [])

  // A click away from any feature centers a box the size of the viewport there
  // and queries it; a click on one shows its properties.
  const { map: mapRef, styleVersion, showPopup } = useMapLibre(container, {
    addLayers,
    featureLayers: FEATURE_LAYERS,
    fullscreen: true,
    onFeatureClick: (_map: maplibregl.Map, hits, point: LngLat) => showPopup(point, propertiesTable(hits[0].properties)),
    onEmptyClick: (map: maplibregl.Map, point: LngLat) => {
      // A map showing a result is not a box query waiting to happen: the box
      // is queried again only once the dropdown is put back to it.
      if (boxLocked.current) return
      const next = viewportBox(map, point)
      setInputs(formatInputs(next))
      void queryBox(next)
    },
  })

  // The extent is read once per visit: it seeds the first box when none has
  // been typed, and it is what the extent toggle draws.
  useEffect(() => {
    const controller = new AbortController()
    fetchExtent(controller.signal)
      .then((feature) => {
        setExtent(feature)
        if (feature.bbox) setInputs((current) => (parseInputs(current) ? current : formatInputs(feature.bbox as BBox)))
      })
      .catch((caught) => {
        if (!controller.signal.aborted) setError(caught instanceof Error ? caught.message : String(caught))
      })
    return () => controller.abort()
  }, [])

  const shown = useMemo(() => {
    if (source === 'bbox') {
      return boxResult
        ? { label: 'bounding box', features: boxResult.features, rowsWithoutGeometry: 0, geometryColumns: [] }
        : null
    }
    const result = converted[source]
    return result && { label: SOURCE_LABELS[source], ...result }
  }, [source, boxResult, converted])

  // Draws whatever is selected and fits the view to it: to the box for a box
  // query, so an empty answer still shows where was asked, else to the features.
  useEffect(() => {
    const map = mapRef.current
    if (styleVersion === 0 || !map) return
    const features = shown?.features ?? []
    setData(map, 'features', { type: 'FeatureCollection', features })
    const target = source === 'bbox' && boxResult ? boxResult.box : boundsOf(features)
    if (target) {
      map.fitBounds([target[0], target[1], target[2], target[3]], { padding: 48, maxZoom: 14, duration: 500 })
    }
  }, [mapRef, styleVersion, shown, source, boxResult])

  useEffect(() => {
    const map = mapRef.current
    if (styleVersion === 0 || !map) return
    // the box belongs to the box query; a map drawing a result does not show it
    setData(map, 'bbox', box && source === 'bbox' ? boxPolygon(box) : EMPTY)
  }, [mapRef, styleVersion, box, source])

  useEffect(() => {
    const map = mapRef.current
    if (styleVersion === 0 || !map) return
    const visible = showExtent && extent?.geometry ? extent : null
    setData(map, 'extent', visible ? (visible as Feature) : EMPTY)
    if (visible?.bbox && !shown?.features.length) {
      map.fitBounds([visible.bbox[0], visible.bbox[1], visible.bbox[2], visible.bbox[3]], { padding: 48, maxZoom: 14 })
    }
  }, [mapRef, styleVersion, showExtent, extent, shown])

  const setInput = (index: number, value: string) => {
    setInputs((current) => current.map((v, i) => (i === index ? value : v)) as BBoxInputs)
  }

  // Hands the box to the SPARQL tab as a GeoSPARQL query, so what the map found
  // can be refined by hand.
  // The same route a share link takes: the router reads the query back out of
  // the URL and opens it in a SPARQL tab of its own.
  const toSparql = () => {
    if (!box) return
    visit(`${pathForTab('SPARQL')}?q=${encodeURIComponent(sparqlBoxQuery(box))}`)
  }

  // The results are mutually exclusive, so they are one dropdown; a result
  // with nothing the map can draw is listed grayed out, and the box query is
  // the choice that querying one selects.
  const drawable = (key: ResultSource) => (converted[key]?.features.length ?? 0) > 0
  const sourceOption = (key: ResultSource) => (
    <option key={key} value={key} disabled={!drawable(key)} title={results[key]?.query}>
      {SOURCE_LABELS[key]}
      {drawable(key) ? ` · ${converted[key]!.features.length}` : ' · nothing to draw'}
    </option>
  )

  return (
    <>
      <header className="panel-header">
        <h3>Geospatial</h3>
        {subtabs}
        <div className="panel-header-actions">
          {slow && <span className="badge">Querying…</span>}
        </div>
        <p>
          Visualize features from the table on a map
        </p>
      </header>
      <div className="map-layout">
        <aside className="map-controls">
          <div className="map-control-group">
            {/* The chips are offered only once an editor has a result to draw. */}
            {(converted.SQL?.features.length || converted.SPARQL?.features.length || converted.Features?.features.length) ? (
              <>
                <label className="chips-label" htmlFor="map-source">
                  Source
                </label>
                <select id="map-source" className="module-input map-source" value={source} onChange={(event) => setSource(event.target.value as MapSource)}>
                  {sourceOption('SQL')}
                  {sourceOption('SPARQL')}
                  {sourceOption('Features')}
                  <option value="bbox">Bounding box query</option>
                </select>
                {shown && source !== 'bbox' && (
                  <p className="hint">
                    Drawing {shown.features.length} {shown.features.length === 1 ? 'feature' : 'features'} from the {shown.label}
                    {shown.geometryColumns.length > 0 && (
                      <>
                        {' using the '}
                        {shown.geometryColumns.map((column, index) => (
                          <span key={column}>
                            {index > 0 && (index === shown.geometryColumns.length - 1 ? ' and ' : ', ')}
                            <code>{column}</code>
                          </span>
                        ))}
                        {shown.geometryColumns.length === 1 ? ' field' : ' fields'}
                      </>
                    )}
                  </p>
                )}
              </>
            ) : null}
            {shown && shown.rowsWithoutGeometry > 0 && (
              <p className="hint">
                {shown.rowsWithoutGeometry} of the rows held no WKT or GeoJSON in any column and are not drawn.
              </p>
            )}
          </div>

          <div className={locked ? 'map-control-group map-control-group-locked' : 'map-control-group'}>
            <span className="chips-label">Bounding box</span>
            <p className='hint'> Click the map or the query button to explore features</p>
            {locked && <p className="hint">Bounding box queries locked while showing other result. Change drop down to switch data source</p>}
            <div className="map-bbox-grid">
              {(['min lon', 'min lat', 'max lon', 'max lat'] as const).map((label, index) => (
                <label key={label} className="map-bbox-field">
                  <span>{label}</span>
                  <input
                    className="module-input"
                    value={inputs[index]}
                    inputMode="decimal"
                    placeholder="0"
                    disabled={locked}
                    onChange={(event) => setInput(index, event.target.value)}
                    onKeyDown={(event) => {
                      if (event.key === 'Enter' && box) void queryBox(box)
                    }}
                  />
                </label>
              ))}
            </div>
            {inputs.some((v) => v.trim()) && !box && (
              <p className="hint">Enter four numbers with the minimums first.</p>
            )}
            <div className="map-actions">
              <button
                type="button"
                className="button primary"
                disabled={!box || loading || locked}
                onClick={() => box && void queryBox(box)}
              >
                Query
              </button>
              <button type="button" className="button" disabled={!box || locked} onClick={toSparql}>
                Open as GeoSPARQL
              </button>
            </div>
            {boxResult && (
              <p className="hint">
                Returned {boxResult.features.length} {boxResult.features.length === 1 ? 'feature' : 'features'} in{' '}
                {boxResult.seconds.toFixed(2)} seconds
              </p>
            )}
          </div>

          {error && <p className="error-banner">{error}</p>}
        </aside>
        <div className="map-stage">
          <div className="map-canvas" ref={container} />
          {/* The extent toggle sits on the map, opposite MapLibre's own controls. */}
          <div className="map-overlay">
            <button
              type="button"
              className="map-overlay-button"
              disabled={!extent?.bbox}
              title={extent?.bbox ? 'Zoom to the spatial extent of the whole table' : 'The table holds no geometries'}
              onClick={() => {
                const map = mapRef.current
                if (map && extent?.bbox) {
                  map.fitBounds([extent.bbox[0], extent.bbox[1], extent.bbox[2], extent.bbox[3]], { padding: 48, maxZoom: 14 })
                }
              }}
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <path d="M4 10V4h6M14 4h6v6M20 14v6h-6M10 20H4v-6" />
                <path d="M4 4l5 5M20 4l-5 5M20 20l-5-5M4 20l5-5" />
              </svg>
            </button>
            <button
              type="button"
              className={showExtent ? 'map-overlay-button active' : 'map-overlay-button'}
              disabled={!extent?.geometry}
              aria-pressed={showExtent}
              title={
                !extent
                  ? 'Reading the spatial extent of the table…'
                  : !extent.geometry
                    ? 'The table holds no geometries'
                    : showExtent
                      ? 'Hide the spatial extent of the whole table'
                      : `Show the spatial extent of the whole table (${extent.properties.geometries} geometries)`
              }
              onClick={() => setShowExtent((current) => !current)}
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <path d="M4 9V5a1 1 0 0 1 1-1h4M15 4h4a1 1 0 0 1 1 1v4M20 15v4a1 1 0 0 1-1 1h-4M9 20H5a1 1 0 0 1-1-1v-4" />
                <path d="M9 12h6M12 9v6" />
              </svg>
            </button>
          </div>
        </div>
      </div>
    </>
  )
}
