import { useEffect, useMemo, useState } from 'react'
import CodeMirror from '@uiw/react-codemirror'
import { PostgreSQL, sql } from '@codemirror/lang-sql'
import { EditorState } from '@codemirror/state'
import { EditorView } from '@codemirror/view'
import { catppuccin } from '../../theme'

/** What the SQL pane has to show for the query the SPARQL editor currently holds. */
export type Translation =
  | { status: 'empty' }
  | { status: 'ok'; sql: string }
  | { status: 'error'; message: string }

/**
 * The read-only view of the DuckDB SQL the SPARQL editor's query translates to.
 * It is a debugging aid rather than a second editor, so it cannot be typed in;
 * the SQL tab is where a statement is edited and run. `height` is the SPARQL
 * editor's own height, so the two read as a pair when they sit side by side.
 */
export function SqlPreview({ translation, height }: { translation: Translation; height: number | null }) {
  const [copied, setCopied] = useState(false)
  const extensions = useMemo(
    () => [
      sql({ dialect: PostgreSQL, upperCaseKeywords: true }),
      EditorState.readOnly.of(true),
      EditorView.editable.of(false),
      ...catppuccin,
    ],
    [],
  )

  useEffect(() => {
    if (!copied) return
    const timer = setTimeout(() => setCopied(false), 1200)
    return () => clearTimeout(timer)
  }, [copied])

  const copySQL = async () => {
    if (translation.status !== 'ok') return
    await navigator.clipboard.writeText(translation.sql)
    setCopied(true)
  }

  return (
    <aside className="sql-preview" aria-label="Translated SQL">
      <header className="sql-preview-header">
        <h4>Translated SQL</h4>
        <span className="hint">read only</span>
        <button
          type="button"
          className={`button sql-preview-copy${copied ? ' copied' : ''}`}
          onClick={() => void copySQL()}
          disabled={translation.status !== 'ok'}
        >
          {copied ? 'Copied' : 'Copy SQL'}
        </button>
      </header>
      <div className="editor sql-preview-editor" style={height ? { height } : undefined}>
        {translation.status === 'empty' && <p className="empty">Type a SPARQL query to see the SQL it runs as.</p>}
        {translation.status === 'error' && <p className="error-banner">{translation.message}</p>}
        {translation.status === 'ok' && (
          <CodeMirror value={translation.sql} height="100%" theme="none" extensions={extensions} editable={false} />
        )}
      </div>
    </aside>
  )
}
