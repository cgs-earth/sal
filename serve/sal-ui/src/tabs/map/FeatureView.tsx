import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type maplibregl from 'maplibre-gl'
import type { LngLat } from 'maplibre-gl'
import type { Feature, FeatureCollection } from 'geojson'
import {
  fetchOgcItem,
  fetchOgcItems,
  fetchVocabularies,
  OGC_COLLECTION,
  OGC_PATH,
  ogcItemURL,
  ogcItemsURL,
  type OgcFeature,
  type OgcQuery,
  type QueryResult,
  type Vocabulary,
} from '../../api'
import { boundsOf } from '../../geo'
import { propertiesTable, useMapLibre } from '../../components/useMapLibre'
import type { MapPalette } from '../../theme'
import type { OgcRequest } from '../../routing'
import { publishResult } from '../../results'
import { MapLinkButton } from '../../components/MapLinkButton'
import { TermInput } from './TermInput'
import { compactTerm } from './terms'
import { describeValue, diffFeatures } from './featureDiff'

/** What one snapshot answered: a page of features, or the one feature asked for, null when it has none. */
type Loaded = { kind: 'page'; features: OgcFeature[]; more: boolean } | { kind: 'item'; feature: OgcFeature | null }

/** A run that resolved: what each snapshot answered and the requests that fetched it. */
type Run = {
  primary: Loaded
  compared: Loaded | null
  /** The snapshot each side was read at, '' for the current table, for the URL a feature is copied as. */
  snapshot: string
  compareSnapshot: string
  /** The property IRIs the run asked for, which are the rows of a feature's property table. */
  properties: string[]
  /** Whether a second snapshot was read, which is always set against the first. */
  diff: boolean
  primaryLabel: string
  compareLabel: string
}

/** The two panes of the view. */
type Pane = 'builder' | 'results'

/** How a feature stands between the two snapshots of a run, null when there is one snapshot. */
type Status = 'only-primary' | 'only-compare' | 'changed' | 'unchanged' | null

/** One row of the results list: the feature at each snapshot and how it stands between them. */
type Row = { id: string; primary: OgcFeature | null; compare: OgcFeature | null; status: Status; differing: Set<string> }

/*
 * The query, the last run, and what is selected in it survive leaving the
 * view and the tab, so a feature looked up is still there on return.
 */
const persisted = {
  snapshot: '',
  compare: '',
  properties: [] as string[],
  featureId: '',
  type: '',
  limit: 100,
  run: null as Run | null,
  pane: 'builder' as Pane,
  selected: '',
  page: 0,
  onlyDifferences: false,
  showQueries: false,
  /** The request the view last ran on arrival, so switching views and back does not run it again. */
  served: null as OgcRequest | null,
}

/** What a run reads: the builder's fields as they stood when Run was pressed, or an OGC URL's. */
type Query = {
  snapshot: string
  compare: string
  properties: string[]
  featureId: string
  type: string
  limit: number
}

const EMPTY: FeatureCollection = { type: 'FeatureCollection', features: [] }

/** The compare select's value for the current table, which the snapshot select says with ''. */
const CURRENT = 'current'

/** How many features the results list shows at a time. */
const PAGE = 5

/** The layers a click on a feature is looked up in, topmost first. */
const FEATURE_LAYERS = ['features-point', 'compare-point', 'features-line', 'compare-line', 'features-fill', 'compare-fill']

const STATUS_LABEL: Record<Exclude<Status, null>, string> = {
  'only-primary': 'only here',
  'only-compare': 'only there',
  changed: 'changed',
  unchanged: 'unchanged',
}

/**
 * Adds the two feature sources, one per snapshot, and their layers. The
 * primary snapshot draws in the feature colors the map view uses, the
 * compared one in the palette's compare color, so the two can be told apart
 * on one map; the compared layers sit under the primary ones.
 */
function addLayers(map: maplibregl.Map, { feature, point, pointStroke, compare }: MapPalette) {
  for (const [source, color, circle] of [
    ['compare', compare, compare],
    ['features', feature, point],
  ] as const) {
    map.addSource(source, { type: 'geojson', data: EMPTY })
    map.addLayer({
      id: `${source}-fill`,
      type: 'fill',
      source,
      filter: ['match', ['geometry-type'], ['Polygon', 'MultiPolygon'], true, false],
      paint: { 'fill-color': color, 'fill-opacity': 0.22 },
    })
    map.addLayer({
      id: `${source}-line`,
      type: 'line',
      source,
      filter: ['match', ['geometry-type'], ['Polygon', 'MultiPolygon', 'LineString', 'MultiLineString'], true, false],
      paint: { 'line-color': color, 'line-width': 2 },
    })
    map.addLayer({
      id: `${source}-point`,
      type: 'circle',
      source,
      filter: ['match', ['geometry-type'], ['Point', 'MultiPoint'], true, false],
      paint: {
        'circle-radius': 6,
        'circle-color': circle,
        'circle-stroke-color': pointStroke,
        'circle-stroke-width': 1.5,
      },
    })
  }
}

/**
 * The snapshot IDs the Stats listing holds, newest first. The option label
 * says when each was committed and what tags it carries; the short one, for
 * the badges and the table headings, leaves the tags out, since a build tags
 * its snapshot with the forty characters of the commit it was built from.
 */
function snapshotOptions(snapshots: QueryResult | null): { id: string; label: string; short: string }[] {
  const header = snapshots?.header ?? []
  const idColumn = header.indexOf('snapshot_id')
  if (idColumn < 0) return []
  const timeColumn = header.indexOf('timestamp_ms')
  const tagsColumn = header.indexOf('tags')
  return (snapshots?.rows ?? [])
    .map((row) => {
      const id = row[idColumn] ?? ''
      const parts = [id]
      if (timeColumn >= 0 && row[timeColumn]) parts.push(row[timeColumn].replace(/\.\d+$/, ''))
      const short = parts.join(' · ')
      if (tagsColumn >= 0 && row[tagsColumn]) parts.push(row[tagsColumn])
      return { id, label: parts.join(' · '), short }
    })
    .filter((option) => option.id !== '')
}

/** The features a Loaded answer holds. */
function featuresOf(loaded: Loaded | null): OgcFeature[] {
  if (!loaded) return []
  if (loaded.kind === 'page') return loaded.features
  return loaded.feature ? [loaded.feature] : []
}

/**
 * The rows of the results list: every feature the primary snapshot answered,
 * in its order, then the ones only the compared snapshot has. With two
 * snapshots each row knows how the feature stands between them and which of
 * its properties differ, which is what the side by side tables highlight.
 */
function rowsOf(run: Run): Row[] {
  const primary = featuresOf(run.primary)
  if (!run.compared) {
    return primary.map((feature) => ({ id: feature.id, primary: feature, compare: null, status: null, differing: new Set() }))
  }
  const diff = diffFeatures(primary, featuresOf(run.compared))
  const byId = new Map<string, Row>()
  for (const feature of diff.onlyPrimary) {
    byId.set(feature.id, { id: feature.id, primary: feature, compare: null, status: 'only-primary', differing: new Set() })
  }
  for (const feature of diff.unchanged) {
    byId.set(feature.id, { id: feature.id, primary: feature, compare: feature, status: 'unchanged', differing: new Set() })
  }
  for (const change of diff.changed) {
    const differing = new Set(change.properties.map((property) => property.key))
    if (change.geometryChanged) differing.add('geometry')
    byId.set(change.primary.id, { id: change.primary.id, primary: change.primary, compare: change.compare, status: 'changed', differing })
  }
  const rows: Row[] = []
  for (const feature of primary) {
    const row = byId.get(feature.id)
    if (row) rows.push(row)
  }
  for (const feature of diff.onlyCompare) {
    rows.push({ id: feature.id, primary: null, compare: feature, status: 'only-compare', differing: new Set() })
  }
  return rows
}

/** The GeoJSON the map draws for a feature: its id among the properties, so the popup names it. */
function toMapFeature(feature: OgcFeature): Feature {
  return {
    type: 'Feature',
    id: feature.id,
    geometry: feature.geometry ?? { type: 'GeometryCollection', geometries: [] },
    properties: { id: feature.id, ...feature.properties },
  }
}

type FeatureViewProps = {
  subtabs: ReactNode
  /** The Stats tab's snapshot listing, which is where the snapshot IDs to choose from come from. */
  snapshots: QueryResult | null
  /** The OGC items or item URL the browser navigated to, run on arrival, or null for a plain visit. */
  request: OgcRequest | null
}

/**
 * The feature view of the Geospatial tab: a query builder over the OGC API Features
 * endpoint and the results of running it, for looking at individual features
 * rather than everything at once. The builder names the snapshot to read, a
 * feature by its IRI or a page of them, and the filters: the predicates to
 * carry as properties, completed from the pinned vocabularies the way the
 * SPARQL editor completes a term, and a class the features must have. A
 * second snapshot can be compared with the first and diffed against it in the
 * browser. Nothing is fetched until the query is run; the view then moves to
 * the results pane, a list of a few feature IRIs at a time, and the
 * properties of the one selected are shown under it, at both snapshots side
 * by side when diffing, with a map of that feature only on request.
 */
export function FeatureView({ subtabs, snapshots, request }: FeatureViewProps) {
  const [vocabularies, setVocabularies] = useState<Vocabulary[] | null>(null)
  const [snapshot, setSnapshot] = useState(persisted.snapshot)
  const [compare, setCompare] = useState(persisted.compare)
  const [properties, setProperties] = useState(persisted.properties)
  const [featureId, setFeatureId] = useState(persisted.featureId)
  const [type, setType] = useState(persisted.type)
  const [limit, setLimit] = useState(persisted.limit)
  const [propertyDraft, setPropertyDraft] = useState('')
  const [typeDraft, setTypeDraft] = useState('')
  // The class box is shown only once asked for, since most queries filter by none.
  const [filterByClass, setFilterByClass] = useState(persisted.type !== '')
  const [run, setRun] = useState<Run | null>(persisted.run)
  const [pane, setPane] = useState<Pane>(persisted.run ? persisted.pane : 'builder')
  const [selected, setSelected] = useState(persisted.selected)
  const [page, setPage] = useState(persisted.page)
  const [onlyDifferences, setOnlyDifferences] = useState(persisted.onlyDifferences)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [copied, setCopied] = useState<string | null>(null)
  const [showQueries, setShowQueries] = useState(persisted.showQueries)
  const container = useRef<HTMLDivElement>(null)

  useEffect(() => {
    Object.assign(persisted, { snapshot, compare, properties, featureId, type, limit, run, pane, selected, page, onlyDifferences, showQueries })
  }, [snapshot, compare, properties, featureId, type, limit, run, pane, selected, page, onlyDifferences, showQueries])

  useEffect(() => {
    const controller = new AbortController()
    fetchVocabularies(controller.signal)
      .then((listing) => setVocabularies(listing.vocabularies))
      .catch(() => setVocabularies([]))
    return () => controller.abort()
  }, [])

  const options = useMemo(() => snapshotOptions(snapshots), [snapshots])
  const snapshotLabel = (id: string) => (id ? (options.find((option) => option.id === id)?.short ?? id) : 'the current table')
  const compareAt = compare === CURRENT ? '' : compare
  const known = vocabularies ?? []
  // What the comparison can be made against: the current table, when the
  // table is being read at a snapshot, and every snapshot but the one being
  // read. The current table is the newest snapshot, so reading it offers the
  // snapshots before that one. Ticking Compare? picks the first of them.
  const compareChoices = [
    ...(snapshot ? [{ id: CURRENT, label: 'Current table' }] : []),
    ...options
      .filter((option, index) => (snapshot ? option.id !== snapshot : index > 0))
      .map((option) => ({ id: option.id, label: option.label })),
  ]

  // Runs the query: the primary snapshot, and the compared one with the same
  // parameters when there is one. A feature one snapshot lacks is null rather
  // than an error, which is what the list reports as only in the other. A run
  // that resolves opens the results pane on its first page.
  const execute = async ({ snapshot, compare, properties, featureId, type, limit }: Query) => {
    const compareAt = compare === CURRENT ? '' : compare
    const query: OgcQuery = { snapshot, properties, type: type || undefined, limit }
    const id = featureId.trim()
    const load = async (at: string): Promise<Loaded> => {
      const q = { ...query, snapshot: at }
      if (id) return { kind: 'item', feature: await fetchOgcItem(id, q) }
      const page = await fetchOgcItems(q)
      return { kind: 'page', features: page.features, more: page.links.some((link) => link.rel === 'next') }
    }
    setLoading(true)
    setError(null)
    try {
      const [primary, compared] = await Promise.all([load(snapshot), compare ? load(compareAt) : Promise.resolve(null)])
      setRun({
        primary,
        compared,
        snapshot,
        compareSnapshot: compareAt,
        properties,
        diff: compare !== '',
        primaryLabel: snapshotLabel(snapshot),
        compareLabel: snapshotLabel(compareAt),
      })
      setSelected(id)
      setPage(0)
      setPane('results')
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught))
    } finally {
      setLoading(false)
    }
  }

  // An OGC URL the browser arrived on is the query, filled into the builder
  // and run, so an item link opens on that feature with its properties and
  // map; the same URL is not run again when the view is left and returned to.
  // execute is read through a ref, since it is remade every render and the
  // effect is about the request alone.
  const runRequest = useRef(execute)
  runRequest.current = execute
  useEffect(() => {
    if (!request || request === persisted.served) return
    persisted.served = request
    const query: Query = {
      snapshot: request.snapshot,
      compare: '',
      properties: request.properties,
      featureId: request.featureId ?? '',
      type: request.type,
      limit: request.limit ?? persisted.limit,
    }
    setSnapshot(query.snapshot)
    setCompare(query.compare)
    setProperties(query.properties)
    setFeatureId(query.featureId)
    setType(query.type)
    setLimit(query.limit)
    void runRequest.current(query)
  }, [request])

  const rows = useMemo(() => (run ? rowsOf(run) : []), [run])
  const listed = useMemo(
    () =>
      run?.diff && onlyDifferences && rows.some((row) => row.status !== 'unchanged')
        ? rows.filter((row) => row.status !== 'unchanged')
        : rows,
    [rows, run, onlyDifferences],
  )
  // The counts of the diff summary; with nothing only in one snapshot or
  // changed, the summary is the one statement that there are no differences.
  const tally = (status: Status) => rows.filter((row) => row.status === status).length
  const differences = tally('only-primary') + tally('only-compare') + tally('changed')
  const pages = Math.max(1, Math.ceil(listed.length / PAGE))
  const current = Math.min(page, pages - 1)
  const shown = listed.slice(current * PAGE, current * PAGE + PAGE)
  const selectedRow = rows.find((row) => row.id === selected) ?? null

  // The run as a table the map view can draw: every feature listed, with its
  // GeoJSON and the properties asked for, so the map button hands it over the
  // way the editors hand over a result.
  const asResult = useMemo(() => {
    if (!run) return null
    const header = ['feature', 'geometry', ...(run.compared ? ['snapshot'] : []), ...run.properties]
    return {
      header,
      rows: rows.map((row) => {
        const feature = (row.primary ?? row.compare) as OgcFeature
        return [
          feature.id,
          JSON.stringify(feature.geometry),
          ...(run.compared ? [row.primary ? run.primaryLabel : run.compareLabel] : []),
          ...run.properties.map((iri) => describeValue(feature.properties[iri])),
        ]
      }),
    }
  }, [run, rows])
  // The map colors are explained only when there are two geometries to tell apart.
  const geometryDiffers = !!run?.diff && !!selectedRow?.differing.has('geometry')

  // The map is drawn for the selected feature alone, under its properties.
  const { map: mapRef, styleVersion, showPopup } = useMapLibre(container, {
    addLayers,
    featureLayers: FEATURE_LAYERS,
    onFeatureClick: (_map: maplibregl.Map, hits, point: LngLat) => showPopup(point, propertiesTable(hits[0].properties)),
    enabled: pane === 'results' && selectedRow !== null,
  })

  useEffect(() => {
    const map = mapRef.current
    if (styleVersion === 0 || !map || !selectedRow) return
    const features = selectedRow.primary ? [toMapFeature(selectedRow.primary)] : []
    const others = run?.diff && selectedRow.compare ? [toMapFeature(selectedRow.compare)] : []
    map.getSource<maplibregl.GeoJSONSource>('features')?.setData({ type: 'FeatureCollection', features })
    map.getSource<maplibregl.GeoJSONSource>('compare')?.setData({ type: 'FeatureCollection', features: others })
    const bounds = boundsOf([...features, ...others])
    if (!bounds) return
    const point = bounds[0] === bounds[2] && bounds[1] === bounds[3]
    map.fitBounds([bounds[0], bounds[1], bounds[2], bounds[3]], { padding: 48, maxZoom: point ? 9 : 14, duration: 500 })
  }, [mapRef, styleVersion, selectedRow, run])

  const copy = async (key: string, text: string) => {
    await navigator.clipboard.writeText(text)
    setCopied(key)
    setTimeout(() => setCopied(null), 1600)
  }

  // The requests the builder would make as it stands, listed under Run when
  // asked for; one per snapshot when two are to be read.
  const draftQuery: OgcQuery = { snapshot, properties, type: type || undefined, limit }
  const draftURL = (at: string) =>
    featureId.trim() ? ogcItemURL(featureId.trim(), { ...draftQuery, snapshot: at }) : ogcItemsURL({ ...draftQuery, snapshot: at })
  const drafts = [
    { reads: snapshotLabel(snapshot), path: draftURL(snapshot) },
    ...(compare ? [{ reads: snapshotLabel(compareAt), path: draftURL(compareAt) }] : []),
  ]

  /** The URL one feature of the results is read at, on the snapshot it was found in. */
  const featureURL = (row: Row) =>
    ogcItemURL(row.id, { snapshot: row.primary || !run ? run?.snapshot : run.compareSnapshot, properties: run?.properties })

  /** The feature as GeoJSON, as the endpoint answered it. */
  const featureGeoJSON = (row: Row) => {
    const feature = (row.primary ?? row.compare) as OgcFeature
    return JSON.stringify({ type: 'Feature', id: feature.id, geometry: feature.geometry, properties: feature.properties }, null, 2)
  }

  const collectionPath = `${OGC_PATH}/collections/${OGC_COLLECTION}${snapshot ? `?snapshot=${snapshot}` : ''}`

  const panes = (
    <div className="subtabs feature-panes" role="tablist" aria-label="Feature view panes">
      <button
        type="button"
        role="tab"
        aria-selected={pane === 'builder'}
        className={pane === 'builder' ? 'subtab active' : 'subtab'}
        onClick={() => setPane('builder')}
      >
        Query builder
      </button>
      <button
        type="button"
        role="tab"
        aria-selected={pane === 'results'}
        className={pane === 'results' ? 'subtab active' : 'subtab'}
        disabled={!run}
        title={run ? undefined : 'Run a query first'}
        onClick={() => setPane('results')}
      >
        {/* The count is every feature the run listed, both snapshots' when two were read. */}
        {run ? `${rows.length} ${rows.length === 1 ? 'Result' : 'Results'}` : 'Results'}
      </button>
    </div>
  )

  return (
    <>
      <header className="panel-header">
        <h3>Geospatial</h3>
        {subtabs}
        <div className="panel-header-actions">
          {loading && <span className="badge">Running…</span>}
        </div>
        <p>
          Look at individual features through the OGC API Features endpoint at{" "}
          <code>{OGC_PATH}</code>: build the query, run it, and pick a feature
          from the results to see its properties. Links:{" "}
          <a href={OGC_PATH} target="_blank" rel="noreferrer">
            landing page
          </a>
          ,{" "}
          <a href={`${OGC_PATH}/conformance`} target="_blank" rel="noreferrer">
            conformance
          </a>
          ,{" "}
          <a href={collectionPath} target="_blank" rel="noreferrer">
            collection
          </a>
          .
        </p>
      </header>
      <div className="feature-view">
        {panes}
        {pane === "builder" ? (
          <form
            className="feature-builder"
            onSubmit={(event) => {
              event.preventDefault();
              void execute({
                snapshot,
                compare,
                properties,
                featureId,
                type,
                limit,
              });
            }}
          >
            <fieldset className="feature-section">
              <legend>Snapshot</legend>
              <div className="feature-row">
                <label className="feature-field">
                  <span>Read the table at</span>
                  <select
                    className="module-input"
                    value={snapshot}
                    onChange={(event) => {
                      setSnapshot(event.target.value);
                      if (
                        compare === event.target.value ||
                        (compare === CURRENT && event.target.value === "")
                      )
                        setCompare("");
                    }}
                  >
                    <option value="">Current table</option>
                    {options.map((option) => (
                      <option key={option.id} value={option.id}>
                        {option.label}
                      </option>
                    ))}
                  </select>
                </label>
                <div className="feature-field feature-toggles">
                  <span>Compare?</span>
                  <label className="toggle">
                    <input
                      type="checkbox"
                      checked={compare !== ""}
                      disabled={compareChoices.length === 0}
                      onChange={(event) =>
                        setCompare(
                          event.target.checked ? compareChoices[0].id : "",
                        )
                      }
                    />
                    Read an additional snapshot and diff
                  </label>
                  {compareChoices.length === 0 && (
                    <p className="hint">
                      The table has no other snapshot to compare with.
                    </p>
                  )}
                </div>
                {compare && (
                  <label className="feature-field">
                    <span>Compare with</span>
                    <select
                      className="module-input"
                      value={compare}
                      onChange={(event) => setCompare(event.target.value)}
                    >
                      {compareChoices.map((choice) => (
                        <option key={choice.id} value={choice.id}>
                          {choice.label}
                        </option>
                      ))}
                    </select>
                  </label>
                )}
              </div>
            </fieldset>

            <fieldset className="feature-section">
              <legend>Feature</legend>
              <div className="feature-row">
                <label className="feature-field feature-field-wide">
                  <span>Feature IRI</span>
                  <input
                    className="module-input"
                    value={featureId}
                    placeholder="A subject IRI for one feature; empty for a page of them"
                    spellCheck={false}
                    onChange={(event) => setFeatureId(event.target.value)}
                  />
                </label>
                <label className="feature-field feature-field-narrow">
                  <span>Page size</span>
                  <input
                    className="module-input"
                    type="number"
                    min={1}
                    max={1000}
                    value={limit}
                    disabled={featureId.trim() !== ""}
                    onChange={(event) =>
                      setLimit(
                        Math.min(
                          1000,
                          Math.max(1, Number(event.target.value) || 1),
                        ),
                      )
                    }
                  />
                </label>
              </div>
            </fieldset>

            <fieldset className="feature-section">
              <legend>Filters</legend>
              <div className="feature-row">
                <div className="feature-field feature-field-wide">
                  <label htmlFor="feature-property">Properties</label>
                  <p className="hint">
                    Selected predicates to request of every feature. Returns
                    null for a property if no such relation exists.
                  </p>
                  {properties.length > 0 && (
                    <div className="chips feature-chips">
                      {properties.map((iri) => (
                        <button
                          key={iri}
                          type="button"
                          className="chip active"
                          title={`${iri}: click to remove`}
                          onClick={() =>
                            setProperties((current) =>
                              current.filter((p) => p !== iri),
                            )
                          }
                        >
                          {compactTerm(iri, known)} ×
                        </button>
                      ))}
                    </div>
                  )}
                  <TermInput
                    id="feature-property"
                    vocabularies={vocabularies}
                    kind="properties"
                    value={propertyDraft}
                    onChange={setPropertyDraft}
                    onCommit={(iri) => {
                      setProperties((current) =>
                        current.includes(iri) ? current : [...current, iri],
                      );
                      setPropertyDraft("");
                    }}
                    placeholder="A prefix and the property, such as schema:name; or a full IRI"
                  />
                </div>
              </div>
              <div className="feature-row">
                <div className="feature-field feature-field-wide">
                  <span>Class</span>
                  <label className="toggle" title="Keep only the features typed with a class">
                    <input
                      type="checkbox"
                      checked={filterByClass}
                      onChange={(event) => {
                        setFilterByClass(event.target.checked)
                        if (!event.target.checked) {
                          setType('')
                          setTypeDraft('')
                        }
                      }}
                    />
                    Only features of a class
                  </label>
                  {filterByClass && (
                    <>
                      {type && (
                        <div className="chips feature-chips">
                          <button type="button" className="chip active" title={`${type}: click to remove`} onClick={() => setType('')}>
                            {compactTerm(type, known)} ×
                          </button>
                        </div>
                      )}
                      <TermInput
                        id="feature-type"
                        vocabularies={vocabularies}
                        kind="classes"
                        value={typeDraft}
                        onChange={setTypeDraft}
                        onCommit={(iri) => {
                          setType(iri)
                          setTypeDraft('')
                        }}
                        placeholder="A prefix and the class, such as schema:Place; or a full IRI"
                      />
                    </>
                  )}
                </div>
              </div>
            </fieldset>

            <div className="feature-row feature-actions">
              <button
                type="submit"
                className="button primary"
                disabled={loading}
              >
                Run
              </button>
              <label
                className="toggle"
                title="List the requests Run would make, with a button to copy each"
              >
                <input
                  type="checkbox"
                  checked={showQueries}
                  onChange={(event) => setShowQueries(event.target.checked)}
                />
                Show queries
              </label>
              {error && <p className="error-banner">{error}</p>}
            </div>
            {showQueries && (
              <table className="result-table feature-table feature-queries">
                <thead>
                  <tr>
                    <th scope="col">reads</th>
                    <th scope="col">request</th>
                    <th scope="col" />
                  </tr>
                </thead>
                <tbody>
                  {drafts.map(({ reads, path }) => (
                    <tr key={path}>
                      <td>{reads}</td>
                      <td>
                        <code>{path}</code>
                      </td>
                      <td className="feature-copy">
                        <CopyIcon
                          copied={copied === path}
                          onClick={() =>
                            void copy(path, `${window.location.origin}${path}`)
                          }
                        />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </form>
        ) : (
          run && (
            <section className="feature-results">
              <div className="feature-list">
                <div className="feature-list-header">
                  <span className="chips-label">Features</span>
                  {run.diff &&
                    (differences === 0 ? (
                      <span className="badge">No differences</span>
                    ) : (
                      <>
                        <span className="badge">
                          {tally("only-primary")} only in {run.primaryLabel}
                        </span>
                        <span className="badge">
                          {tally("only-compare")} only in {run.compareLabel}
                        </span>
                        <span className="badge">
                          {tally("changed")} changed
                        </span>
                        <span className="badge">
                          {tally("unchanged")} unchanged
                        </span>
                        <label className="toggle">
                          <input
                            type="checkbox"
                            checked={onlyDifferences}
                            onChange={(event) =>
                              setOnlyDifferences(event.target.checked)
                            }
                          />
                          Only what differs
                        </label>
                      </>
                    ))}
                  {rows.length > 1 && asResult && (
                    <MapLinkButton
                      source="Features"
                      header={asResult.header}
                      rows={asResult.rows}
                      title="Show all features on map"
                      onBeforeVisit={() => publishResult({ source: 'Features', query: ogcItemsURL({ snapshot: run.snapshot, properties: run.properties }), ...asResult })}
                    />
                  )}
                </div>
                {listed.length === 0 ? (
                  <p className="hint">
                    {run.primary.kind === "item" && !run.primary.feature
                      ? `No subject with this IRI has a geometry at ${run.primaryLabel}.`
                      : "No features answered this query."}
                  </p>
                ) : (
                  <>
                    <table className="result-table feature-table">
                      <thead>
                        <tr>
                          <th className="row-number" scope="col">
                            #
                          </th>
                          <th scope="col">feature</th>
                          {run.compared && (
                            <th scope="col">between the snapshots</th>
                          )}
                          <th scope="col" />
                        </tr>
                      </thead>
                      <tbody>
                        {shown.map((row, index) => (
                          <tr
                            key={row.id}
                            className={
                              row.id === selected ? "selected" : undefined
                            }
                            aria-selected={row.id === selected}
                            onClick={() =>
                              setSelected(row.id === selected ? "" : row.id)
                            }
                            title="Show this feature's properties"
                          >
                            <td className="row-number">
                              {current * PAGE + index + 1}
                            </td>
                            <td>
                              <code>{row.id}</code>
                            </td>
                            {run.compared && (
                              <td
                                className={`feature-status feature-status-${row.status}`}
                              >
                                {row.status && STATUS_LABEL[row.status]}
                              </td>
                            )}
                            {/* The buttons stop the click, so copying does not also select the row. */}
                            <td
                              className="feature-copy"
                              onClick={(event) => event.stopPropagation()}
                            >
                              <button
                                type="button"
                                className={
                                  copied === `${row.id} url`
                                    ? "button small copied"
                                    : "button small"
                                }
                                title={featureURL(row)}
                                onClick={() =>
                                  void copy(
                                    `${row.id} url`,
                                    `${window.location.origin}${featureURL(row)}`,
                                  )
                                }
                              >
                                {copied === `${row.id} url`
                                  ? "Copied"
                                  : "Copy URL"}
                              </button>
                              <button
                                type="button"
                                className={
                                  copied === `${row.id} geojson`
                                    ? "button small copied"
                                    : "button small"
                                }
                                onClick={() =>
                                  void copy(
                                    `${row.id} geojson`,
                                    featureGeoJSON(row),
                                  )
                                }
                              >
                                {copied === `${row.id} geojson`
                                  ? "Copied"
                                  : "Copy GeoJSON"}
                              </button>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                    <div className="feature-pager">
                      <button
                        type="button"
                        className="button small"
                        disabled={current === 0}
                        onClick={() => setPage(current - 1)}
                      >
                        Previous
                      </button>
                      <span className="hint">
                        {current * PAGE + 1}–
                        {Math.min(listed.length, (current + 1) * PAGE)} of{" "}
                        {listed.length}
                        {run.primary.kind === "page" &&
                          run.primary.more &&
                          " on this page of the endpoint"}
                      </span>
                      <button
                        type="button"
                        className="button small"
                        disabled={current >= pages - 1}
                        onClick={() => setPage(current + 1)}
                      >
                        Next
                      </button>
                    </div>
                  </>
                )}
              </div>

              {selectedRow ? (
                <div className="feature-detail">
                  {run.properties.length === 0 ? (
                    <>
                      <p className="hint">
                        No properties were requested. Add properties in the
                        query builder.
                      </p>
                      {/* with no table headings to carry the colors, the legend stands on its own */}
                      {geometryDiffers && (
                        <p className="feature-legend">
                          <span className="feature-swatch feature-swatch-primary" />{" "}
                          {run.primaryLabel}
                          <span className="feature-swatch feature-swatch-compare" />{" "}
                          {run.compareLabel}
                        </p>
                      )}
                    </>
                  ) : (
                    <>
                      <div className="feature-list-header feature-detail-header">
                        <span className="chips-label">Properties of</span>
                        <code className="feature-detail-id">
                          {selectedRow.id}
                        </code>
                      </div>
                      {geometryDiffers && (
                        <p className="hint">
                          The geometry differs between the two snapshots; both
                          are drawn on the map in these colors.
                        </p>
                      )}
                      <div
                        className={
                          run.diff
                            ? "feature-detail-grid side-by-side"
                            : "feature-detail-grid"
                        }
                      >
                        <PropertyTable
                          feature={selectedRow.primary}
                          properties={run.properties}
                          label={run.diff ? run.primaryLabel : null}
                          swatch={geometryDiffers ? "primary" : null}
                          differing={run.diff ? selectedRow.differing : null}
                          known={known}
                        />
                        {run.diff && (
                          <PropertyTable
                            feature={selectedRow.compare}
                            properties={run.properties}
                            label={run.compareLabel}
                            swatch={geometryDiffers ? "compare" : null}
                            differing={selectedRow.differing}
                            known={known}
                          />
                        )}
                      </div>
                    </>
                  )}
                  <div className="map-canvas feature-map" ref={container} />
                </div>
              ) : (
                listed.length > 0 && (
                  <p className="hint">
                    Select a feature to see its properties.
                  </p>
                )
              )}
            </section>
          )
        )}
      </div>
    </>
  );
}

/** An icon button that copies something to the clipboard, a clipboard that turns into a check mark once it has. */
function CopyIcon({ copied, onClick }: { copied: boolean; onClick: () => void }) {
  const label = copied ? 'Copied' : 'Copy the URL to the clipboard'
  return (
    <button type="button" className={copied ? 'icon-button copied' : 'icon-button'} title={label} aria-label={label} onClick={onClick}>
      {copied ? (
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M5 12.5l4.5 4.5L19 7.5" />
        </svg>
      ) : (
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <rect x="8" y="4" width="12" height="14" rx="2" />
          <path d="M16 18v1a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V9a2 2 0 0 1 2-2h1" />
        </svg>
      )}
    </button>
  )
}

type PropertyTableProps = {
  /** The feature at this snapshot, or null when the snapshot lacks it. */
  feature: OgcFeature | null
  /** The property IRIs the run asked for, one row each. */
  properties: string[]
  /** The snapshot the table shows, as a heading, when two are side by side. */
  label: string | null
  /** The color the map draws this snapshot's geometry in, shown on the heading when the geometries differ. */
  swatch: 'primary' | 'compare' | null
  /** The property keys that differ between the snapshots, highlighted; null when there is nothing to differ from. */
  differing: Set<string> | null
  known: Vocabulary[]
}

/**
 * One feature's properties at one snapshot as a two column table, one row
 * per property the run asked for, with the rows that differ from the other
 * snapshot marked when there is one. The geometry is on the map, not here.
 */
function PropertyTable({ feature, properties, label, swatch, differing, known }: PropertyTableProps) {
  return (
    <div className="feature-props">
      {label && (
        <h4>
          {swatch && <span className={`feature-swatch feature-swatch-${swatch}`} />}
          {label}
        </h4>
      )}
      {feature ? (
        <table className="result-table feature-table feature-props-table">
          <tbody>
            {properties.map((iri) => (
              <tr key={iri} className={differing?.has(iri) ? 'changed' : undefined}>
                <th scope="row" title={iri}>
                  <code>{compactTerm(iri, known)}</code>
                </th>
                <td>{describeValue(feature.properties[iri])}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <p className="hint">Not present at this snapshot.</p>
      )}
    </div>
  )
}
