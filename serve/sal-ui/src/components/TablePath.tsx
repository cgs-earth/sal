import { useEffect, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'
import { REMOTE_TABLE_SCHEMES, switchTable } from '../api'

type TablePathProps = {
  /** The path of the table the server reads now, as the stats report it. */
  tablePath: string
  /** The remote table the server was pointed at, or '' when it reads the project's own. */
  remoteTablePath: string
  /** Called once the server has switched, with the remote table it now reads or '' for the local one. */
  onSwitched: (remoteTablePath: string) => void
}

/**
 * The table path at the right of the header. Clicking it opens an input for
 * the root of a triples table on object storage, the gs://bucket/sal/triples
 * `sal upload` wrote, say, which points every query the UI runs at that table
 * instead of the one on disk. An empty input returns to the local table. The
 * server refuses a path on its own machine, so only the remote schemes are
 * offered.
 */
export function TablePath({ tablePath, remoteTablePath, onSwitched }: TablePathProps) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(remoteTablePath)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const input = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (editing) input.current?.select()
  }, [editing])

  const open = () => {
    setDraft(remoteTablePath)
    setError(null)
    setEditing(true)
  }

  const close = () => {
    setEditing(false)
    setError(null)
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const location = draft.trim()
    if (location === remoteTablePath) {
      close()
      return
    }
    setBusy(true)
    try {
      const table = await switchTable(location)
      onSwitched(table.remoteTablePath)
      close()
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught))
    } finally {
      setBusy(false)
    }
  }

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Escape') close()
  }

  if (!editing) {
    const remote = remoteTablePath !== ''
    return (
      <div className="header-table">
        <button
          type="button"
          className="header-path"
          onClick={open}
          title={`${tablePath}\nClick to query a table on object storage instead`}
        >
          <span className={remote ? 'table-kind remote' : 'table-kind'}>{remote ? 'remote' : 'local'}</span>
          <code>{tablePath}</code>
        </button>
      </div>
    )
  }

  return (
    <div className="header-table">
      {/* The input is named and left open to autocomplete so the browser keeps
          the tables entered before in its form history and offers them again;
          it records an entry when the form is submitted, which still happens
          with the default prevented. */}
      <form className="header-table-form" onSubmit={(event) => void submit(event)}>
        <input
          ref={input}
          id="remote-table-path"
          name="remoteTablePath"
          className="module-input"
          type="text"
          autoComplete="on"
          value={draft}
          spellCheck={false}
          disabled={busy}
          placeholder="gs://bucket/sal/triples"
          aria-label="Table on object storage to query"
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={onKeyDown}
        />
        <button
          type="submit"
          className={`icon-button header-table-confirm${busy ? ' loading' : ''}`}
          aria-label={busy ? 'Opening the table' : 'Query this table'}
          title={busy ? 'Opening the table…' : 'Query this table (Enter)'}
          disabled={busy}
        >
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M5 12.5l4.5 4.5L19 7.5" />
          </svg>
        </button>
        <button
          type="button"
          className="icon-button"
          aria-label="Cancel"
          title="Cancel (Escape)"
          onClick={close}
          disabled={busy}
        >
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" aria-hidden="true">
            <path d="M6.5 6.5l11 11M17.5 6.5l-11 11" />
          </svg>
        </button>
      </form>
      {error ? (
        <p className="error-banner header-table-error">{error}</p>
      ) : (
        <p className="hint header-table-hint">
          {REMOTE_TABLE_SCHEMES.map((scheme, index) => (
            <span key={scheme}>
              {index > 0 && ', '}
              <code>{scheme}</code>
            </span>
          ))}{' '}
          only; leave empty to return to the local table
        </p>
      )}
    </div>
  )
}
