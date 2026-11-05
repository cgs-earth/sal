import { useEffect, useMemo, useRef, useState } from 'react'
import CodeMirror from '@uiw/react-codemirror'
import { PostgreSQL, sql } from '@codemirror/lang-sql'
import { EditorState } from '@codemirror/state'
import { EditorView } from '@codemirror/view'
import { editorExtensions } from '../../theme'
import { explainSql, type QueryPlan } from '../../api'
import { PlanTree } from './PlanTree'

/** What the SQL pane has to show for the query the SPARQL editor currently holds. */
export type Translation =
  | { status: 'empty' }
  | { status: 'ok'; sql: string }
  | { status: 'error'; message: string }

/** The pane's views: the SQL itself, or one of the plans DuckDB would run it as. */
type View = 'sql' | 'logical' | 'optimized' | 'physical'

const VIEWS: { name: View; label: string; title: string }[] = [
  { name: 'sql', label: 'SQL', title: 'The DuckDB SQL the query translates to, read only' },
  { name: 'logical', label: 'Logical', title: 'The logical plan DuckDB binds the SQL to, before the optimizer' },
  { name: 'optimized', label: 'Optimized', title: 'The logical plan after the optimizer has rewritten it' },
  { name: 'physical', label: 'Physical', title: 'The physical operators DuckDB would run' },
]

/** Where the pane's view is remembered between visits. */
const VIEW_KEY = 'sal-ui.sparql.sqlView'

function readView(): View {
  try {
    const stored = localStorage.getItem(VIEW_KEY)
    return VIEWS.some((view) => view.name === stored) ? (stored as View) : 'sql'
  } catch {
    return 'sql'
  }
}

/** The plans fetched for one SQL statement, keyed by that statement so a view switch refetches nothing. */
type Plan =
  | { status: 'idle'; sql?: undefined }
  | { status: 'loading'; sql: string }
  | { status: 'ok'; sql: string; plan: QueryPlan }
  | { status: 'error'; sql: string; message: string }

/**
 * The read-only view of the DuckDB SQL the SPARQL editor's query translates to,
 * with the plans DuckDB would run it as behind the strip in its header. It is a
 * debugging aid rather than a second editor, so it cannot be typed in; the SQL
 * tab is where a statement is edited and run. A plan is fetched only while a
 * plan view is showing, since explaining a statement binds it against the
 * table. The pane is as tall as what it shows, the SQL or the whole plan,
 * rather than a box the content scrolls inside.
 */
export function SqlPreview({ translation }: { translation: Translation }) {
  const [view, setView] = useState<View>(readView)
  const [plan, setPlan] = useState<Plan>({ status: 'idle' })
  const planAbort = useRef<AbortController | null>(null)
  const [copied, setCopied] = useState(false)
  const extensions = useMemo(
    () => [
      sql({ dialect: PostgreSQL, upperCaseKeywords: true }),
      EditorState.readOnly.of(true),
      EditorView.editable.of(false),
      // The pane grows to the SQL rather than scrolling it, so a long
      // projection list wraps instead of running off the right edge.
      EditorView.lineWrapping,
      ...editorExtensions,
    ],
    [],
  )

  const chooseView = (next: View) => {
    setView(next)
    try {
      localStorage.setItem(VIEW_KEY, next)
    } catch {
      // Storage may be unavailable; the pane still follows the strip for this visit.
    }
  }

  // The statement a plan is wanted for: the translation, while a plan view is
  // showing. A statement already fetched, or being fetched, is not asked for
  // again; a new one replaces whatever request is in flight.
  const wanted = view !== 'sql' && translation.status === 'ok' ? translation.sql : null
  useEffect(() => {
    if (wanted === null || plan.sql === wanted) return
    planAbort.current?.abort()
    const controller = new AbortController()
    planAbort.current = controller
    setPlan({ status: 'loading', sql: wanted })
    explainSql(wanted, controller.signal).then(
      (result) => {
        if (!controller.signal.aborted) setPlan({ status: 'ok', sql: wanted, plan: result })
      },
      (caught: unknown) => {
        if (controller.signal.aborted) return
        setPlan({ status: 'error', sql: wanted, message: caught instanceof Error ? caught.message : String(caught) })
      },
    )
  }, [wanted, plan.sql])
  useEffect(() => () => planAbort.current?.abort(), [])

  useEffect(() => {
    if (!copied) return
    const timer = setTimeout(() => setCopied(false), 1200)
    return () => clearTimeout(timer)
  }, [copied])

  // What the copy button puts on the clipboard: the SQL, or the shown plan as JSON.
  const copyable =
    translation.status !== 'ok' ? null
    : view === 'sql' ? translation.sql
    : plan.status === 'ok' && plan.sql === translation.sql ? JSON.stringify(plan.plan[view], null, 2)
    : null
  const copy = async () => {
    if (copyable === null) return
    await navigator.clipboard.writeText(copyable)
    setCopied(true)
  }

  const body = () => {
    if (translation.status === 'empty') return <p className="empty">Type a SPARQL query to see the SQL it runs as.</p>
    if (translation.status === 'error') return <p className="error-banner">{translation.message}</p>
    if (view === 'sql') {
      return <CodeMirror value={translation.sql} height="auto" theme="none" extensions={extensions} editable={false} />
    }
    if (plan.status === 'error') return <p className="error-banner">{plan.message}</p>
    if (plan.status !== 'ok' || plan.sql !== translation.sql) return <p className="empty">Asking DuckDB for the plan…</p>
    return <PlanTree roots={plan.plan[view]} />
  }

  return (
    <aside className="sql-preview" aria-label="Translated SQL">
      <header className="sql-preview-header">
        <div className="subtabs sql-preview-views" role="tablist" aria-label="SQL pane views">
          {VIEWS.map((candidate) => (
            <button
              key={candidate.name}
              type="button"
              role="tab"
              aria-selected={view === candidate.name}
              className={view === candidate.name ? 'subtab active' : 'subtab'}
              title={candidate.title}
              onClick={() => chooseView(candidate.name)}
            >
              {candidate.label}
            </button>
          ))}
        </div>
        <button
          type="button"
          className={`button sql-preview-copy${copied ? ' copied' : ''}`}
          onClick={() => void copy()}
          disabled={copyable === null}
        >
          {copied ? 'Copied' : view === 'sql' ? 'Copy SQL' : 'Copy plan'}
        </button>
      </header>
      <div className={view === 'sql' ? 'editor sql-preview-editor' : 'sql-preview-editor sql-preview-plan'}>{body()}</div>
    </aside>
  )
}
