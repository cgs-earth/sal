import { lazy, Suspense, useCallback, useEffect, useState } from 'react'
import { fetchStats, type TableStats } from './api'
import { StatsTab } from './tabs/StatsTab'
import { SqlTab } from './tabs/SqlTab'
import { ModulesTab } from './tabs/ModulesTab'
import { BlobsTab } from './tabs/BlobsTab'
import { TABS, useRoute } from './routing'
import './App.css'

// YASGUI and its CodeMirror 5 bundle dominate the build, so keep them out of the
// initial chunk and load them the first time the SPARQL tab is opened.
const SparqlTab = lazy(() => import('./tabs/SparqlTab').then((module) => ({ default: module.SparqlTab })))
// MapLibre is nearly as large, and only the Map tab needs it.
const MapTab = lazy(() => import('./tabs/MapTab').then((module) => ({ default: module.MapTab })))

export function App() {
  const { tab: active, sharedQuery, blobHash, renderBlob, navigate } = useRoute()
  const [stats, setStats] = useState<TableStats | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  const loadStats = useCallback(async (signal?: AbortSignal) => {
    setLoading(true)
    try {
      setStats(await fetchStats(signal))
      setError(null)
    } catch (caught) {
      if (signal?.aborted) return
      setError(caught instanceof Error ? caught.message : String(caught))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    void loadStats(controller.signal)
    return () => controller.abort()
  }, [loadStats])

  return (
    <div className="app">
      <header className="app-header">
        <div className="brand">
          <span className="brand-mark">SAL</span>
          <span className="brand-sub">semantic accessibility layer</span>
        </div>
        {stats && (
          <div className="header-meta">
            <span className="badge">{new Intl.NumberFormat().format(stats.triples)} triples</span>
            <code className="header-path" title={stats.tablePath}>
              {stats.tablePath}
            </code>
          </div>
        )}
      </header>

      <nav className="tabs" role="tablist">
        {TABS.map((name) => (
          <button
            key={name}
            type="button"
            role="tab"
            aria-selected={active === name}
            className={active === name ? 'tab active' : 'tab'}
            onClick={() => navigate(name)}
          >
            {name}
          </button>
        ))}
      </nav>

      <main className="content">
        {active === 'Stats' && (
          <StatsTab stats={stats} error={error} loading={loading} onReload={() => void loadStats()} />
        )}
        {active === 'SQL' && (
          <SqlTab
            tablePath={stats?.tablePath ?? null}
            sampleQueries={stats?.sampleQueries ?? null}
            importedTables={stats?.importedTables ?? null}
            sharedQuery={sharedQuery}
          />
        )}
        {/* YASGUI restores its own query state from localStorage, so remounting is safe. */}
        {active === 'SPARQL' && (
          <Suspense fallback={<p className="empty">Loading the SPARQL editor…</p>}>
            <SparqlTab sharedQuery={sharedQuery} />
          </Suspense>
        )}
        {active === 'Modules' && <ModulesTab modules={stats?.modules ?? null} />}
        {/* Keyed on the route so that moving between two blob links remounts the
            tab on the new digest rather than leaving the old one in the form. */}
        {active === 'Blobs' && <BlobsTab key={`${blobHash ?? ''}|${renderBlob}`} hash={blobHash} render={renderBlob} />}
        {active === 'Map' && (
          <Suspense fallback={<p className="empty">Loading the map…</p>}>
            <MapTab />
          </Suspense>
        )}
      </main>
    </div>
  )
}
