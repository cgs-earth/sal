import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import CodeMirror from '@uiw/react-codemirror'
import { PostgreSQL, sql } from '@codemirror/lang-sql'
import { keymap } from '@codemirror/view'
import { Prec } from '@codemirror/state'
import { runSQL, type QueryResult } from '../api'
import { ResultTable } from '../components/ResultTable'
import { toCSV } from '../csv'
import { catppuccin } from '../theme'

const DEFAULT_SQL = 'SELECT *\nFROM triples\nLIMIT 20'

/** Columns of the `triples` view, offered as autocompletions. */
const TRIPLES_COLUMNS = [
  'triple_hash',
  'subject',
  'predicate',
  'object',
  'object_iri',
  'object_string',
  'object_float',
  'object_geometry',
]

type Sample = { name: string; sql: string }

function samplesFor(tablePath: string | null): Sample[] {
  const samples: Sample[] = [
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
  return samples
}

export function SqlTab({ tablePath }: { tablePath: string | null }) {
  const [statement, setStatement] = useState(DEFAULT_SQL)
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
      setResult(await runSQL(text))
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

  const extensions = useMemo(
    () => [
      sql({ dialect: PostgreSQL, upperCaseKeywords: true, schema: { triples: TRIPLES_COLUMNS } }),
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
    [],
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
            The Iceberg table is registered as the <code>triples</code> view.
          </p>
        </header>
        <div className="chips">
          {samplesFor(tablePath).map((sample) => (
            <button key={sample.name} type="button" className="chip" onClick={() => setStatement(sample.sql)}>
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
