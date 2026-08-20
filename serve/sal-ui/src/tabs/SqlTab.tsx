import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import CodeMirror from '@uiw/react-codemirror'
import { PostgreSQL, sql } from '@codemirror/lang-sql'
import { keymap } from '@codemirror/view'
import { Prec } from '@codemirror/state'
import { runSQL, type ImportedTable, type NamedQuery, type QueryResult } from '../api'
import { ResultTable } from '../components/ResultTable'
import { ShareLinkButton } from '../components/ShareLinkButton'
import { toCSV } from '../csv'
import { catppuccin } from '../theme'
import { publishResult } from '../results'

const DEFAULT_SQL = 'SELECT *\nFROM triples\nLIMIT 20'

/** Columns of the `triples` view, offered as autocompletions. */
const TRIPLES_COLUMNS = [
  'triple_hash',
  'subject',
  'predicate',
  'object_iri',
  'object_string',
  'object_float',
  'object_geometry',
]

/**
 * The sample statements offered as chips. The time travel and imported artifact
 * samples are built by the server rather than here: `iceberg_scan` only accepts
 * a literal snapshot ID, and the artifact behind an imported view is known to
 * sal rather than to DuckDB.
 */
function samplesFor(tablePath: string | null, sampleQueries: NamedQuery[] | null): NamedQuery[] {
  const samples: NamedQuery[] = [
    { name: 'Head', sql: DEFAULT_SQL },
    { name: 'Schema', sql: 'DESCRIBE triples' },
    {
      name: 'Predicate counts',
      sql: 'SELECT\n\tpredicate,\n\tCOUNT(*) AS count\nFROM triples\nGROUP BY predicate\nORDER BY count DESC',
    },
    {
      name: 'Busiest subjects',
      sql: 'SELECT\n\tsubject,\n\tCOUNT(*) AS count\nFROM triples\nGROUP BY subject\nORDER BY count DESC\nLIMIT 50',
    },
    {
      name: 'Typed resources',
      sql: "SELECT *\nFROM triples\nWHERE predicate = 'http://www.w3.org/1999/02/22-rdf-syntax-ns#type'\nLIMIT 50",
    },
    {
      name: 'Geometries',
      sql: 'SELECT\n\tsubject,\n\tST_AsText(object_geometry) AS wkt,\n\tST_GeometryType(object_geometry) AS type\nFROM triples\nWHERE object_geometry IS NOT NULL\nLIMIT 500',
    },
  ]
  if (tablePath) {
    const escaped = tablePath.replaceAll("'", "''")
    samples.push(
      {
        name: 'Snapshots',
        sql: `SELECT *\nFROM iceberg_snapshots('${escaped}')\nORDER BY sequence_number DESC`,
      },
      { name: 'Column stats', sql: `SELECT *\nFROM iceberg_column_stats('${escaped}')` },
    )
  }
  return samples.concat(sampleQueries ?? [])
}

export function SqlTab({
  tablePath,
  sampleQueries,
  importedTables,
  sharedQuery,
}: {
  tablePath: string | null
  sampleQueries: NamedQuery[] | null
  importedTables: ImportedTable[] | null
  /** The statement a `/sql?q=` share link opened this tab with, if any. */
  sharedQuery: string | null
}) {
  const [statement, setStatement] = useState(sharedQuery ?? DEFAULT_SQL)
  const [result, setResult] = useState<QueryResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [running, setRunning] = useState(false)
  const [copied, setCopied] = useState(false)

  const run = useCallback(async (text: string) => {
    if (!text.trim()) {
      setError('Enter a SQL statement')
      return
    }
    setRunning(true)
    setError(null)
    try {
      const next = await runSQL(text)
      setResult(next)
      // The Map tab draws whatever geometry the last result held.
      publishResult({ source: 'SQL', query: text, header: next.header ?? [], rows: next.rows ?? [] })
    } catch (caught) {
      setResult(null)
      setError(caught instanceof Error ? caught.message : String(caught))
    } finally {
      setRunning(false)
    }
  }, [])

  // The editor keymap is created once, so it reads the live statement through a ref.
  const statementRef = useRef(statement)
  statementRef.current = statement
  const runRef = useRef(run)
  runRef.current = run

  // Every imported data product has the same columns as the project's own
  // table, so each one completes like `triples` does.
  const schema = useMemo(() => {
    const views: Record<string, string[]> = { triples: TRIPLES_COLUMNS }
    for (const table of importedTables ?? []) views[table.view] = TRIPLES_COLUMNS
    return views
  }, [importedTables])

  const extensions = useMemo(
    () => [
      sql({ dialect: PostgreSQL, upperCaseKeywords: true, schema }),
      Prec.highest(
        keymap.of([
          {
            key: 'Mod-Enter',
            run: () => {
              void runRef.current(statementRef.current)
              return true
            },
          },
        ]),
      ),
      ...catppuccin,
    ],
    [schema],
  )

  useEffect(() => {
    if (!copied) return
    const timer = setTimeout(() => setCopied(false), 1200)
    return () => clearTimeout(timer)
  }, [copied])

  const copyCSV = async () => {
    if (!result) return
    await navigator.clipboard.writeText(toCSV(result.header, result.rows))
    setCopied(true)
  }

  return (
    <div className="tab-body split">
      <section className="panel editor-panel">
        <header className="panel-header">
          <h3>DuckDB SQL</h3>
          <p>
            The Iceberg table is registered as the <code>triples</code> view
            {importedTables && importedTables.length > 0 && (
              <>
                , each imported data product as{' '}
                {importedTables.map((table, index) => (
                  <span key={table.view}>
                    {index > 0 && ', '}
                    <code title={table.artifact}>{table.view}</code>
                  </span>
                ))}
                , and all of them together as <code>imports</code>
              </>
            )}
            .
          </p>
        </header>
        <div className="chips">
          {samplesFor(tablePath, sampleQueries).map((sample) => (
            <button
              key={sample.name}
              type="button"
              className="chip"
              title={sample.sql}
              onClick={() => setStatement(sample.sql)}
            >
              {sample.name}
            </button>
          ))}
        </div>
        <div className="editor">
          {/* theme="none" keeps the package from injecting its default light theme,
              which would otherwise win over the Catppuccin theme in `extensions`. */}
          <CodeMirror
            value={statement}
            height="100%"
            theme="none"
            extensions={extensions}
            onChange={setStatement}
          />
        </div>
        <div className="toolbar">
          <button type="button" className="button primary" onClick={() => void run(statement)} disabled={running}>
            {running ? 'Running…' : 'Run'}
          </button>
          <span className="hint">
            <kbd>⌘</kbd>/<kbd>Ctrl</kbd> + <kbd>Enter</kbd>
          </span>
          <span className="toolbar-actions">
            <ShareLinkButton tab="SQL" query={() => statementRef.current} />
          </span>
        </div>
      </section>

      <section className="panel results-panel">
        <header className="panel-header">
          <h3>Results</h3>
          <div className="panel-header-actions">
            {result && <span className="badge">{result.message}</span>}
            {result && (
              <button type="button" className="button" onClick={() => void copyCSV()}>
                {copied ? 'Copied' : 'Copy CSV'}
              </button>
            )}
          </div>
        </header>
        {error && <p className="error-banner">{error}</p>}
        {!error && !result && <p className="empty">Run a statement to see results here.</p>}
        {!error && result && <ResultTable header={result.header} rows={result.rows} />}
      </section>
    </div>
  )
}
