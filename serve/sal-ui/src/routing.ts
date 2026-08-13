import { useCallback, useEffect, useState } from 'react'

export const TABS = ['Stats', 'SQL', 'SPARQL', 'Modules', 'Blobs', 'Map'] as const
export type TabName = (typeof TABS)[number]

/**
 * The path each tab lives at, so a tab can be linked to rather than only
 * clicked to. `/sparql` and `/blobs` are also API routes; the server tells a
 * browser navigation apart from an API call and serves this app only to the
 * former, so both names can mean both things.
 */
const TAB_PATHS: Record<TabName, string> = {
  Stats: '/stats',
  SQL: '/sql',
  SPARQL: '/sparql',
  Modules: '/modules',
  Blobs: '/blobs',
  Map: '/map',
}

/** The parameter a shared query is carried in. */
const QUERY_PARAM = 'q'

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

export type Route = {
  tab: TabName
  /** The query a share link seeded this tab with, or null for a plain visit. */
  sharedQuery: string | null
}

function readLocation(): Route {
  return {
    tab: tabForPath(window.location.pathname),
    sharedQuery: new URLSearchParams(window.location.search).get(QUERY_PARAM),
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
    setRoute({ tab, sharedQuery: null })
  }, [])

  return { ...route, navigate }
}
