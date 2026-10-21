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
      <form className="header-table-form" onSubmit={(event) => void submit(event)}>
        <input
          ref={input}
          className="module-input"
          type="text"
          value={draft}
          spellCheck={false}
          disabled={busy}
          placeholder="gs://bucket/sal/triples"
          aria-label="Table on object storage to query"
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={onKeyDown}
        />
        <button type="submit" className="button small primary" disabled={busy}>
          {busy ? 'Opening…' : 'Query'}
        </button>
        <button type="button" className="button small" onClick={close} disabled={busy}>
          Cancel
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
