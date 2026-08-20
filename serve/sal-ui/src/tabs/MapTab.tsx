import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import maplibregl, { type GeoJSONSource, type LngLat, type StyleSpecification } from 'maplibre-gl'
import 'maplibre-gl/dist/maplibre-gl.css'
import type { Feature, FeatureCollection } from 'geojson'
import { fetchExtent, fetchGeometries, type BBox, type GeoJSONFeature } from '../api'
import { boundsOf, boxAround, boxPolygon, featuresFromResult, type MapFeature } from '../geo'
import { useResults, type ResultSource } from '../results'

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
  showBox: boolean
  showExtent: boolean
} = { source: null, inputs: null, showBox: true, showExtent: false }

/** Width of the box a click draws, in degrees, before any grow or shrink. */
const DEFAULT_BOX_SIZE = 1

const EMPTY: FeatureCollection = { type: 'FeatureCollection', features: [] }

/*
 * A raster basemap rather than a vector style, since it needs no glyphs or
 * sprites to work and a single dark tile set matches the rest of the UI. The
 * features still draw when the tiles cannot be fetched, only over black.
 */
const BASEMAP: StyleSpecification = {
  version: 8,
  sources: {
    basemap: {
      type: 'raster',
      tiles: ['a', 'b', 'c', 'd'].map((host) => `https://${host}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}.png`),
      tileSize: 256,
      attribution:
        '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors &copy; <a href="https://carto.com/attributions">CARTO</a>',
    },
  },
  layers: [{ id: 'basemap', type: 'raster', source: 'basemap' }],
}

// Catppuccin Mocha, matching theme.ts; MapLibre paint wants literal colors, not CSS variables.
const SKY = '#89dceb'
const PEACH = '#fab387'
const MAUVE = '#cba6f7'
const GREEN = '#a6e3a1'
const CRUST = '#11111b'

/** The layers a click on a feature is looked up in, topmost first. */
const FEATURE_LAYERS = ['features-point', 'features-line', 'features-fill']

function addLayers(map: maplibregl.Map) {
  map.addSource('extent', { type: 'geojson', data: EMPTY })
  map.addSource('bbox', { type: 'geojson', data: EMPTY })
  map.addSource('features', { type: 'geojson', data: EMPTY })
  map.addLayer({
    id: 'extent-fill',
    type: 'fill',
    source: 'extent',
    paint: { 'fill-color': GREEN, 'fill-opacity': 0.08 },
  })
  map.addLayer({
    id: 'extent-line',
    type: 'line',
    source: 'extent',
    paint: { 'line-color': GREEN, 'line-width': 1.5, 'line-dasharray': [4, 3] },
  })
  map.addLayer({
    id: 'features-fill',
    type: 'fill',
    source: 'features',
    filter: ['match', ['geometry-type'], ['Polygon', 'MultiPolygon'], true, false],
    paint: { 'fill-color': SKY, 'fill-opacity': 0.22 },
  })
  map.addLayer({
    id: 'features-line',
    type: 'line',
    source: 'features',
    filter: ['match', ['geometry-type'], ['Polygon', 'MultiPolygon', 'LineString', 'MultiLineString'], true, false],
    paint: { 'line-color': SKY, 'line-width': 2 },
  })
  map.addLayer({
    id: 'features-point',
    type: 'circle',
    source: 'features',
    filter: ['match', ['geometry-type'], ['Point', 'MultiPoint'], true, false],
    paint: {
      'circle-radius': 6,
      'circle-color': PEACH,
      'circle-stroke-color': CRUST,
      'circle-stroke-width': 1.5,
    },
  })
  map.addLayer({
    id: 'bbox-line',
    type: 'line',
    source: 'bbox',
    paint: { 'line-color': MAUVE, 'line-width': 2, 'line-dasharray': [2, 2] },
  })
}

function setData(map: maplibregl.Map, source: string, data: FeatureCollection | Feature) {
  const geojson = map.getSource<GeoJSONSource>(source)
  geojson?.setData(data)
}

/** Renders a feature's properties as a table for its popup, built as DOM so a value is never parsed as HTML. */
function propertiesTable(properties: Record<string, unknown>): HTMLElement {
  const table = document.createElement('table')
  table.className = 'map-popup-table'
  for (const [key, value] of Object.entries(properties)) {
    const row = table.insertRow()
    const name = document.createElement('th')
    name.textContent = key
    row.appendChild(name)
    const cell = row.insertCell()
    const text = String(value ?? '')
    if (/^https?:\/\//.test(text)) {
      const link = document.createElement('a')
      link.href = text
      link.target = '_blank'
      link.rel = 'noreferrer'
      link.textContent = text
      cell.appendChild(link)
    } else {
      cell.textContent = text
    }
  }
  return table
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

export function MapTab() {
  const results = useResults()
  const container = useRef<HTMLDivElement>(null)
  const mapRef = useRef<maplibregl.Map | null>(null)
  const popupRef = useRef<maplibregl.Popup | null>(null)
  const [ready, setReady] = useState(false)

  const [source, setSource] = useState<MapSource>(() => persisted.source ?? results.latest ?? 'bbox')
  const [inputs, setInputs] = useState<BBoxInputs>(() => persisted.inputs ?? ['', '', '', ''])
  const [showBox, setShowBox] = useState(persisted.showBox)
  const [showExtent, setShowExtent] = useState(persisted.showExtent)
  const [extent, setExtent] = useState<GeoJSONFeature | null>(null)
  const [boxResult, setBoxResult] = useState<{ box: BBox; features: MapFeature[] } | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    persisted.source = source
    persisted.inputs = inputs
    persisted.showBox = showBox
    persisted.showExtent = showExtent
  }, [source, inputs, showBox, showExtent])

  const box = useMemo(() => parseInputs(inputs), [inputs])

  // Each editor's result is converted once per result rather than per render,
  // since the chips show the counts of both whichever one is drawn.
  const converted = useMemo(
    () => ({
      SQL: results.SQL && featuresFromResult(results.SQL.header, results.SQL.rows),
      SPARQL: results.SPARQL && featuresFromResult(results.SPARQL.header, results.SPARQL.rows),
    }),
    [results],
  )

  // Runs the bounding box query for a box and shows its features.
  const queryBox = useCallback(async (target: BBox) => {
    setSource('bbox')
    setLoading(true)
    setError(null)
    try {
      const collection = await fetchGeometries({ bbox: target, limit: 1000 })
      setBoxResult({ box: target, features: collection.features.filter((f) => f.geometry) as MapFeature[] })
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught))
    } finally {
      setLoading(false)
    }
  }, [])

  // A click away from any feature centers a box of the current size there and
  // queries it. Registered once on the map, so it reads the live box through a ref.
  const onMapClick = useRef<(point: LngLat) => void>(() => {})
  onMapClick.current = (point) => {
    const size = box ? Math.max(box[2] - box[0], box[3] - box[1]) || DEFAULT_BOX_SIZE : DEFAULT_BOX_SIZE
    const next = boxAround(point.lng, point.lat, size)
    setInputs(formatInputs(next))
    void queryBox(next)
  }

  useEffect(() => {
    const parent = container.current
    if (!parent) return
    const map = new maplibregl.Map({
      container: parent,
      style: BASEMAP,
      center: [0, 20],
      zoom: 1,
      attributionControl: { compact: true },
    })
    map.addControl(new maplibregl.NavigationControl({ showCompass: false }), 'top-right')
    map.addControl(new maplibregl.ScaleControl(), 'bottom-left')
    // A basemap tile that cannot be fetched is not worth a console error per
    // tile; the features draw without it.
    map.on('error', (event) => {
      if (!('tile' in event)) console.error(event.error)
    })
    map.on('load', () => {
      addLayers(map)
      setReady(true)
    })
    map.on('click', (event) => {
      const hits = map.queryRenderedFeatures(event.point, { layers: FEATURE_LAYERS })
      popupRef.current?.remove()
      if (hits.length === 0) {
        onMapClick.current(event.lngLat)
        return
      }
      popupRef.current = new maplibregl.Popup({ maxWidth: '420px', className: 'map-popup' })
        .setLngLat(event.lngLat)
        .setDOMContent(propertiesTable(hits[0].properties))
        .addTo(map)
    })
    for (const layer of FEATURE_LAYERS) {
      map.on('mouseenter', layer, () => {
        map.getCanvas().style.cursor = 'pointer'
      })
      map.on('mouseleave', layer, () => {
        map.getCanvas().style.cursor = ''
      })
    }
    mapRef.current = map
    return () => {
      popupRef.current?.remove()
      popupRef.current = null
      mapRef.current = null
      setReady(false)
      map.remove()
    }
  }, [])

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
    return result && { label: `${source} result`, ...result }
  }, [source, boxResult, converted])

  // Draws whatever is selected and fits the view to it: to the box for a box
  // query, so an empty answer still shows where was asked, else to the features.
  useEffect(() => {
    const map = mapRef.current
    if (!ready || !map) return
    const features = shown?.features ?? []
    setData(map, 'features', { type: 'FeatureCollection', features })
    const target = source === 'bbox' && boxResult ? boxResult.box : boundsOf(features)
    if (target) {
      map.fitBounds([target[0], target[1], target[2], target[3]], { padding: 48, maxZoom: 14, duration: 500 })
    }
  }, [ready, shown, source, boxResult])

  useEffect(() => {
    const map = mapRef.current
    if (!ready || !map) return
    setData(map, 'bbox', showBox && box ? boxPolygon(box) : EMPTY)
  }, [ready, showBox, box])

  useEffect(() => {
    const map = mapRef.current
    if (!ready || !map) return
    const visible = showExtent && extent?.geometry ? extent : null
    setData(map, 'extent', visible ? (visible as Feature) : EMPTY)
    if (visible?.bbox && !shown?.features.length) {
      map.fitBounds([visible.bbox[0], visible.bbox[1], visible.bbox[2], visible.bbox[3]], { padding: 48, maxZoom: 14 })
    }
  }, [ready, showExtent, extent, shown])

  const setInput = (index: number, value: string) => {
    setInputs((current) => current.map((v, i) => (i === index ? value : v)) as BBoxInputs)
  }

  const fitExtent = () => {
    const map = mapRef.current
    if (map && extent?.bbox) {
      map.fitBounds([extent.bbox[0], extent.bbox[1], extent.bbox[2], extent.bbox[3]], { padding: 48, maxZoom: 14 })
    }
  }

  // Hands the box to the SPARQL tab as a GeoSPARQL query, so what the map found
  // can be refined by hand.
  // The same route a share link takes: the router reads the query back out of
  // the URL and opens it in a SPARQL tab of its own.
  const toSparql = () => {
    if (!box) return
    window.history.pushState(null, '', `/sparql?q=${encodeURIComponent(sparqlBoxQuery(box))}`)
    window.dispatchEvent(new PopStateEvent('popstate'))
  }

  // A result with nothing the map can draw is offered grayed out, saying why.
  const sourceChip = (key: ResultSource, label: string) => {
    const result = converted[key]
    const drawable = !!result && result.features.length > 0
    return (
      <button
        key={key}
        type="button"
        className={source === key ? 'chip active' : 'chip'}
        disabled={!drawable}
        title={drawable ? results[key]?.query : 'No geometry to render'}
        onClick={() => setSource(key)}
      >
        {label}
        {drawable && ` · ${result.features.length}`}
      </button>
    )
  }

  return (
    <div className="tab-body map">
      <section className="panel map-panel">
        <header className="panel-header">
          <h3>Map</h3>
          <div className="panel-header-actions">
            {shown && <span className="badge">{`${shown.features.length} features from the ${shown.label}`}</span>}
            {loading && <span className="badge">Querying…</span>}
          </div>
          <p>
            Draws the geometry a query returned, or everything inside a box. Click the map to query the box around that
            point.
          </p>
        </header>
        <div className="map-layout">
          <aside className="map-controls">
            <div className="map-control-group">
              <span className="chips-label">Show</span>
              <div className="chips map-chips">
                {sourceChip('SQL', 'SQL result')}
                {sourceChip('SPARQL', 'SPARQL result')}
              </div>
              {shown && shown.geometryColumns.length > 0 && (
                <p className="hint">
                  Geometry read from{' '}
                  {shown.geometryColumns.map((column, index) => (
                    <span key={column}>
                      {index > 0 && ', '}
                      <code>{column}</code>
                    </span>
                  ))}
                  ; the other columns are the feature&rsquo;s properties, shown when it is clicked.
                </p>
              )}
              {shown && shown.rowsWithoutGeometry > 0 && (
                <p className="hint">
                  {shown.rowsWithoutGeometry} of the rows held no WKT or GeoJSON in any column and are not drawn.
                </p>
              )}
              {!shown && (
                <p className="hint">
                  Run a query on the SQL or SPARQL tab whose result includes WKT or GeoJSON, or click the map to query
                  the box around that point.
                </p>
              )}
            </div>

            <div className="map-control-group">
              <span className="chips-label">Bounding box</span>
              <div className="map-bbox-grid">
                {(['min lon', 'min lat', 'max lon', 'max lat'] as const).map((label, index) => (
                  <label key={label} className="map-bbox-field">
                    <span>{label}</span>
                    <input
                      className="module-input"
                      value={inputs[index]}
                      inputMode="decimal"
                      placeholder="0"
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
                  disabled={!box || loading}
                  onClick={() => box && void queryBox(box)}
                >
                  Query box
                </button>
              </div>
              <label className="map-toggle">
                <input type="checkbox" checked={showBox} onChange={(event) => setShowBox(event.target.checked)} />
                Outline the box on the map
              </label>
              <button type="button" className="button map-link" disabled={!box} onClick={toSparql}>
                Open as a GeoSPARQL query
              </button>
            </div>

            <div className="map-control-group">
              <span className="chips-label">Dataset</span>
              <label className="map-toggle">
                <input
                  type="checkbox"
                  checked={showExtent}
                  disabled={!extent?.geometry}
                  onChange={(event) => setShowExtent(event.target.checked)}
                />
                Show the spatial extent of the whole table
                {extent && ` (${extent.properties.geometries} geometries)`}
              </label>
              <div className="map-actions">
                <button type="button" className="button" disabled={!extent?.bbox} onClick={fitExtent}>
                  Zoom to extent
                </button>
                <button
                  type="button"
                  className="button"
                  disabled={!extent?.bbox}
                  onClick={() => extent?.bbox && setInputs(formatInputs(extent.bbox))}
                >
                  Use extent as box
                </button>
              </div>
              {extent && !extent.geometry && <p className="hint">The table holds no geometries.</p>}
            </div>

            {error && <p className="error-banner">{error}</p>}
          </aside>
          <div className="map-canvas" ref={container} />
        </div>
      </section>
    </div>
  )
}
