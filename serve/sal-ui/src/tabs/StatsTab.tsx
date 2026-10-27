import { lazy, Suspense, useEffect, useState } from 'react'
import { STAC_COLLECTION_PATH, hasStacCatalog, stacViewerURL, type TableStats } from '../api'
import { ResultTable } from '../components/ResultTable'

type StatsTabProps = {
  stats: TableStats | null
  error: string | null
  loading: boolean
  onReload: () => void
}

// MapLibre is the largest thing the UI loads, so the card's small map comes in
// its own chunk, shared with the Map tab, rather than in the page's.
const ExtentMap = lazy(() =>
  import('../components/ExtentMap').then((module) => ({
    default: module.ExtentMap,
  })),
)

const formatCount = (value: number) => new Intl.NumberFormat().format(value)

export function StatsTab({ stats, error, loading, onReload }: StatsTabProps) {
  // A table built before sal wrote STAC catalogs has none to open, so the links
  // are offered only once the collection has answered.
  const [stacAvailable, setStacAvailable] = useState(false)
  useEffect(() => {
    const controller = new AbortController()
    hasStacCatalog(controller.signal)
      .then(setStacAvailable)
      .catch(() => setStacAvailable(false))
    return () => controller.abort()
  }, [stats])

  return (
    <div className="tab-body stats">
      {error && <p className="error-banner">{error}</p>}
      {!stats && loading && <p className="empty">Reading Iceberg metadata…</p>}

      {stats && (
        <>
          {/* The landing card: what the data product is, how big it is, and the
              STAC catalog describing it. The Iceberg detail follows below it;
              the tab bar is what leads to the queries. */}
          <section className="panel dataset-card" aria-labelledby="dataset-title">
            <div className="dataset-main">
              <div className="dataset-identity">
                <div className="dataset-meta-row">
                  <p className="dataset-meta" id="dataset-title">
                    <span className="label">Name</span>
                    <code className="dataset-name">{stats.title || tableName(stats.tablePath)}</code>
                    <span className="label">Path</span>
                    <code className="dataset-path" title={stats.tablePath}>
                      {stats.tablePath}
                    </code>
                  </p>
                  <div className="dataset-actions">
                    <StacLink
                      href={STAC_COLLECTION_PATH}
                      available={stacAvailable}
                      title="The STAC Collection sal build wrote for this table"
                    >
                      STAC JSON
                    </StacLink>
                    <StacLink
                      href={stacViewerURL(window.location.origin)}
                      available={stacAvailable}
                      title="Open the catalog in the Portolan STAC Browser"
                    >
                      STAC viewer
                    </StacLink>
                    <button
                      type="button"
                      className={`icon-button refresh-button${loading ? ' loading' : ''}`}
                      aria-label={loading ? 'Refreshing' : 'Refresh'}
                      title="Reread the table's counts and metadata"
                      onClick={onReload}
                      disabled={loading}
                    >
                      <svg
                        viewBox="0 0 24 24"
                        fill="none"
                        stroke="currentColor"
                        strokeWidth="1.8"
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        aria-hidden="true"
                      >
                        <path d="M20 12a8 8 0 1 1-2.6-5.9" />
                        <path d="M20 4v5h-5" />
                      </svg>
                    </button>
                  </div>
                </div>
              </div>

              <dl className="dataset-figures">
                <div className="figure headline">
                  <dd>{formatCount(stats.triples)}</dd>
                  <dt>Triples</dt>
                </div>
                <div className="figure">
                  <dd>{formatCount(stats.subjects)}</dd>
                  <dt>Distinct subjects</dt>
                </div>
                <div className="figure">
                  <dd>{formatCount(stats.predicates)}</dd>
                  <dt>Distinct predicates</dt>
                </div>
                <div className="figure">
                  <dd>{formatCount(stats.objects)}</dd>
                  <dt>Distinct objects</dt>
                </div>
              </dl>
            </div>

            {/* The right column is the extent map alone, so it takes exactly the
                height the name, description, and figures beside it need and
                never adds any of its own. */}
            <Suspense
              fallback={
                <figure className="dataset-extent">
                  <div className="extent-map" />
                  <figcaption>Spatial Extent</figcaption>
                </figure>
              }
            >
              <ExtentMap refreshKey={stats} />
            </Suspense>
          </section>

          <Section title="Snapshots" caption="Every commit written to the Iceberg table, newest first.">
            <ResultTable header={stats.snapshots.header} rows={stats.snapshots.rows} empty="No snapshots yet" />
          </Section>

          <Section title="Vocabularies" caption="Every RDF vocabulary that is imported or pinned in the project">
            <ResultTable
              header={stats.vocabularies.header}
              rows={stats.vocabularies.rows}
              empty="No vocabularies found; run sal import to import one, or sal build/validate to pin the vocabularies a project resolves against"
            />
          </Section>

          <Section title="Table properties" caption="Iceberg metadata properties from the latest metadata file.">
            <ResultTable header={stats.properties.header} rows={stats.properties.rows} empty="No properties set" />
          </Section>

          <Section title="Column statistics" caption="Per-column value counts and bounds recorded by Iceberg.">
            <ResultTable header={stats.columnStats.header} rows={stats.columnStats.rows} empty="No column statistics" />
          </Section>
        </>
      )}
    </div>
  )
}

/** The last path segment of the table, which names a project whose ontology has no title. */
function tableName(tablePath: string) {
  const segments = tablePath.replace(/\/+$/, '').split('/')
  return segments[segments.length - 1] || tablePath
}

function Section({ title, caption, children }: { title: string; caption: string; children: React.ReactNode }) {
  return (
    <section className="panel">
      <header className="panel-header">
        <h3>{title}</h3>
        <p>{caption}</p>
      </header>
      {children}
    </section>
  )
}

type StacLinkProps = {
  href: string
  available: boolean
  title: string
  children: string
}

/** A link to a STAC document, disabled with a hint when no catalog has been built. */
function StacLink({ href, available, title, children }: StacLinkProps) {
  return (
    <a
      className="button small"
      href={href}
      target="_blank"
      rel="noreferrer"
      aria-disabled={!available}
      tabIndex={available ? undefined : -1}
      title={available ? title : 'No STAC catalog has been built for this table; run sal build to write one'}
    >
      {children}
    </a>
  )
}
