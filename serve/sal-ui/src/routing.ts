import { useCallback, useEffect, useState } from 'react'
import type { ResultSource } from './results'

export const TABS = ['Stats', 'SPARQL', 'SQL', 'Geospatial', 'Blobs'] as const
export type TabName = (typeof TABS)[number]

/**
 * The path each tab lives at, so a tab can be linked to rather than only
 * clicked to. `/sparql` and `/blobs` are also API routes; the server tells a
 * browser navigation apart from an API call and serves this app only to the
 * former, so both names can mean both things.
 */
const TAB_PATHS: Record<TabName, string> = {
  Stats: '/stats',
  SPARQL: '/sparql',
  SQL: '/sql',
  Geospatial: '/geospatial',
  Blobs: '/blobs',
}

/** The two views of the Geospatial tab. */
export type MapView = 'map' | 'features'

/**
 * The OGC API Features endpoint's root. The server answers a browser's
 * navigation to any of its routes with this app, which opens the Geospatial tab on
 * the feature view, the human readable face of that endpoint, and runs the
 * request an items or item URL names. `/geospatial?view=features` is the same view
 * with nothing to run.
 */
const OGC_ROOT = '/ogc'
/** The Geospatial tab's path before it was renamed, still honored. */
const OLD_MAP_PATH = '/map'
const VIEW_PARAM = 'view'

/** The items route and the item route under it, the id escaped into one segment. */
const OGC_ITEMS = /^\/ogc\/collections\/triples\/items(?:\/([^/]+))?\/?$/

/**
 * A request of the OGC API Features endpoint a browser navigated to, as the
 * feature view's query: the one feature named, or a page, at a snapshot,
 * with the properties and the class the URL asked for. The parameters the
 * builder has no field for, bbox and offset, are left out.
 */
export type OgcRequest = {
  /** The subject IRI of the one feature, or null for a page. */
  featureId: string | null
  /** The snapshot ID, '' for the current table. */
  snapshot: string
  properties: string[]
  type: string
  /** The page size, or null when the URL did not say. */
  limit: number | null
}

/** The request an OGC items or item URL names, or null for any other location. */
export function ogcRequestForLocation(pathname: string, params: URLSearchParams): OgcRequest | null {
  const match = OGC_ITEMS.exec(pathname)
  if (!match) return null
  const limit = Number.parseInt(params.get('limit') ?? '', 10)
  return {
    featureId: match[1] ? decodeURIComponent(match[1]) : null,
    snapshot: params.get('snapshot') ?? '',
    properties: (params.get('properties') ?? '')
      .split(',')
      .map((iri) => iri.trim())
      .filter(Boolean),
    type: params.get('type') ?? '',
    limit: Number.isFinite(limit) && limit > 0 ? limit : null,
  }
}

/** The parameter a shared query is carried in. */
const QUERY_PARAM = 'q'
/** The parameter the Blobs tab reads the digest to open from. */
const BLOB_PARAM = 'hash'

export function pathForTab(tab: TabName): string {
  return TAB_PATHS[tab]
}

/** The tab a path names, defaulting to the first one for `/` and anything unknown. */
export function tabForPath(pathname: string): TabName {
  const normalized = pathname.replace(/\/+$/, '').toLowerCase() || '/'
  if (normalized === OGC_ROOT || normalized.startsWith(OGC_ROOT + '/')) return 'Geospatial'
  // the tab was /map before it was named for what it holds; a link to that still opens it
  if (normalized === OLD_MAP_PATH) return 'Geospatial'
  return TABS.find((tab) => TAB_PATHS[tab] === normalized) ?? TABS[0]
}

/** The parameter naming which editor's result the map view should draw. */
const SOURCE_PARAM = 'source'

/** The map view drawing an editor's result, which the editors' map buttons open. */
export function mapLink(source: ResultSource): string {
  return `${TAB_PATHS.Geospatial}?${VIEW_PARAM}=map&${SOURCE_PARAM}=${source}`
}

/** The editor's result a location asks the map view to draw, or null. */
export function mapSourceForLocation(params: URLSearchParams): ResultSource | null {
  const source = params.get(SOURCE_PARAM)
  return source === 'SQL' || source === 'SPARQL' || source === 'Features' ? source : null
}

/** The view of the Geospatial tab a location asks for, or null when it names none. */
export function mapViewForLocation(pathname: string, params: URLSearchParams): MapView | null {
  const normalized = pathname.replace(/\/+$/, '').toLowerCase()
  if (normalized === OGC_ROOT || normalized.startsWith(OGC_ROOT + '/') || params.get(VIEW_PARAM) === 'features') return 'features'
  if ((normalized === TAB_PATHS.Geospatial || normalized === OLD_MAP_PATH) && params.get(VIEW_PARAM) === 'map') return 'map'
  return null
}

/**
 * An absolute link that reopens tab with query already loaded into its editor.
 * The query rides in `q` rather than the SPARQL Protocol's own `query`, which
 * stays reserved for callers of the `/sparql` endpoint itself.
 */
export function shareLink(tab: TabName, query: string): string {
  return `${window.location.origin}${pathForTab(tab)}?${QUERY_PARAM}=${encodeURIComponent(query)}`
}

/**
 * The Blobs tab URL for a digest, which opens the tab on that blob's own view
 * rather than on the listing; without a digest it is the listing itself.
 */
export function blobLink(hash: string): string {
  if (!hash) return pathForTab('Blobs')
  const params = new URLSearchParams()
  params.set(BLOB_PARAM, hash)
  return `${pathForTab('Blobs')}?${params}`
}

/**
 * Follows an in-app link. The router only listens for popstate, so the
 * navigation is pushed and then announced the same way the back button is.
 */
export function visit(url: string) {
  window.history.pushState(null, '', url)
  window.dispatchEvent(new PopStateEvent('popstate'))
}

export type Route = {
  tab: TabName
  /** The query a share link seeded this tab with, or null for a plain visit. */
  sharedQuery: string | null
  /** The digest the Blobs tab was opened on, or null for the listing. */
  blobHash: string | null
  /** The view the Geospatial tab was opened on, or null when the location named none. */
  mapView: MapView | null
  /** The OGC request the feature view was opened on, to run on arrival, or null. */
  ogcRequest: OgcRequest | null
  /** The editor's result the map view was opened on, to draw on arrival, or null. */
  mapSource: ResultSource | null
  /**
   * Counts the navigations, so that following the same link twice is two
   * arrivals: a view or a source asked for again is acted on again even
   * though nothing else about the route changed.
   */
  arrival: number
}

let arrivals = 0

function readLocation(): Route {
  const params = new URLSearchParams(window.location.search)
  arrivals += 1
  return {
    tab: tabForPath(window.location.pathname),
    sharedQuery: params.get(QUERY_PARAM),
    blobHash: params.get(BLOB_PARAM),
    mapView: mapViewForLocation(window.location.pathname, params),
    ogcRequest: ogcRequestForLocation(window.location.pathname, params),
    mapSource: mapSourceForLocation(params),
    arrival: arrivals,
  }
}

/**
 * Keeps the active tab and the address bar in step, so that every tab is a URL
 * that can be shared and that the browser's back button returns to.
 */
export function useRoute(): Route & { navigate: (tab: TabName) => void } {
  const [route, setRoute] = useState<Route>(readLocation)

  useEffect(() => {
    const onPopState = () => setRoute(readLocation())
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [])

  const navigate = useCallback((tab: TabName) => {
    // The search is dropped rather than carried along: a query shared into one
    // tab means nothing in another, and leaving it in the URL would offer it to
    // whichever tab is opened next.
    const path = pathForTab(tab)
    if (window.location.pathname !== path || window.location.search) {
      window.history.pushState(null, '', path)
    }
    setRoute({ tab, sharedQuery: null, blobHash: null, mapView: null, ogcRequest: null, mapSource: null, arrival: ++arrivals })
  }, [])

  return { ...route, navigate }
}
