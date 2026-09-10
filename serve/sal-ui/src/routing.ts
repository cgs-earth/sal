import { useCallback, useEffect, useState } from 'react'

export const TABS = ['Stats', 'SPARQL', 'SQL', 'Map', 'Blobs', 'Modules'] as const
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
  Map: '/map',
  Blobs: '/blobs',
  Modules: '/modules',
}

/** The parameter a shared query is carried in. */
const QUERY_PARAM = 'q'
/** The parameter the Blobs tab reads the digest to look up from. */
const BLOB_PARAM = 'hash'
/** The parameter that asks the Blobs tab to show a blob in the page instead of downloading it. */
const RENDER_PARAM = 'render'

export function pathForTab(tab: TabName): string {
  return TAB_PATHS[tab]
}

/** The tab a path names, defaulting to the first one for `/` and anything unknown. */
export function tabForPath(pathname: string): TabName {
  const normalized = pathname.replace(/\/+$/, '').toLowerCase() || '/'
  return TABS.find((tab) => TAB_PATHS[tab] === normalized) ?? TABS[0]
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
 * The Blobs tab URL for a digest. `render=true` shows the blob in the page,
 * which is the right default for a link to an ontology document since those
 * are meant to be read; it is left off otherwise, since most blobs are not,
 * and the tab defaults to downloading.
 */
export function blobLink(hash: string, render: boolean): string {
  const params = new URLSearchParams()
  if (hash) params.set(BLOB_PARAM, hash)
  if (render) params.set(RENDER_PARAM, 'true')
  const search = params.toString()
  return search ? `${pathForTab('Blobs')}?${search}` : pathForTab('Blobs')
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
  /** The digest the Blobs tab was opened on, or null for a plain visit. */
  blobHash: string | null
  /** Whether the Blobs tab was asked to render the blob in the page rather than download it. */
  renderBlob: boolean
}

function readLocation(): Route {
  const params = new URLSearchParams(window.location.search)
  return {
    tab: tabForPath(window.location.pathname),
    sharedQuery: params.get(QUERY_PARAM),
    blobHash: params.get(BLOB_PARAM),
    renderBlob: params.get(RENDER_PARAM) === 'true',
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
    setRoute({ tab, sharedQuery: null, blobHash: null, renderBlob: false })
  }, [])

  return { ...route, navigate }
}
