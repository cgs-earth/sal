import type { TableStats } from '../api'
import { ResultTable } from '../components/ResultTable'

type StatsTabProps = {
  stats: TableStats | null
  error: string | null
  loading: boolean
  onReload: () => void
}

const formatCount = (value: number) => new Intl.NumberFormat().format(value)

export function StatsTab({ stats, error, loading, onReload }: StatsTabProps) {
  return (
    <div className="tab-body stats">
      <div className="toolbar">
        <h2>Table statistics</h2>
        <div className="toolbar-actions">
          <button type="button" className="button" onClick={onReload} disabled={loading}>
            {loading ? 'Refreshing…' : 'Refresh'}
          </button>
        </div>
      </div>

      {error && <p className="error-banner">{error}</p>}
      {!stats && loading && <p className="empty">Reading Iceberg metadata…</p>}

      {stats && (
        <>
          <p className="table-path" title={stats.tablePath}>
            <span className="label">Table</span>
            <code>{stats.tablePath}</code>
          </p>

          <div className="stat-grid">
            <StatCard label="Triples" value={formatCount(stats.triples)} accent="lavender" />
            <StatCard label="Distinct subjects" value={formatCount(stats.subjects)} accent="blue" />
            <StatCard label="Distinct predicates" value={formatCount(stats.predicates)} accent="teal" />
            <StatCard label="Distinct objects" value={formatCount(stats.objects)} accent="peach" />
            <StatCard label="Snapshots" value={formatCount(stats.snapshots.rows?.length ?? 0)} accent="mauve" />
          </div>

          <Section title="Snapshots" caption="Every commit written to the Iceberg table, newest first.">
            <ResultTable header={stats.snapshots.header} rows={stats.snapshots.rows} empty="No snapshots yet" />
          </Section>

          <Section
            title="Vocabularies"
            caption="Every vocabulary sal build/validate has pinned, unioned with what sal import has recorded — the same listing sal get vocabularies prints."
          >
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

function StatCard({ label, value, accent }: { label: string; value: string; accent: string }) {
  return (
    <div className="stat-card" data-accent={accent}>
      <span className="stat-value">{value}</span>
      <span className="stat-label">{label}</span>
    </div>
  )
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
