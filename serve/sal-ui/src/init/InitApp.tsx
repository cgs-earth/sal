import { useEffect, useState } from 'react'
import { ThemeToggle } from '../components/ThemeToggle'
import { fetchStatus, type ModuleStatus } from './api'
import { ModuleForm } from './ModuleForm'
import '../App.css'
import './init.css'

/** How often the module root is checked for ontology.jsonld appearing, disappearing, or changing. */
const STATUS_POLL_MS = 2000

/**
 * The page `sal init --salmodule` and `sal edit` open: the form that writes a
 * module's ontology.jsonld, starting from the one the module has. The server
 * reads the disk on every status request, so a file written or deleted by
 * hand is seen here; the form itself is kept while the file exists, so that
 * an edit in progress survives the poll, and starts over when the file is
 * created or removed outside it.
 */
export function InitApp() {
  const [status, setStatus] = useState<ModuleStatus | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [showHelp, setShowHelp] = useState(false)

  useEffect(() => {
    const controller = new AbortController()
    const load = async () => {
      try {
        setStatus(await fetchStatus(controller.signal))
        setError(null)
      } catch (caught) {
        if (controller.signal.aborted) return
        setError(caught instanceof Error ? caught.message : String(caught))
      }
    }
    void load()
    const timer = setInterval(() => void load(), STATUS_POLL_MS)
    return () => {
      clearInterval(timer)
      controller.abort()
    }
  }, [])

  return (
    <div className="app">
      <header className="app-header">
        <div className="brand">
          <a className="brand-mark" href="https://cgs-earth.github.io/sal/" target="_blank" rel="noreferrer" title="SAL documentation">
            SAL
          </a>
          <span className="brand-sub">{status?.mode === 'edit' ? 'module edit' : 'module init'}</span>
        </div>
        <div className="header-actions">
          <button
            type="button"
            className={`button small help-button${showHelp ? ' active' : ''}`}
            aria-expanded={showHelp}
            title="How this page works"
            onClick={() => setShowHelp((value) => !value)}
          >
            ?
          </button>
          <ThemeToggle />
        </div>
      </header>

      {showHelp && (
        <div className="help-panel" role="note">
          <ul>
            <li>
              A SAL module is a git repository with a <code>Dockerfile</code> and an <code>ontology.jsonld</code>{' '}
              declaring the tasks a project can run. This page writes and edits the <code>ontology.jsonld</code>; the
              first write also scaffolds the Dockerfile and editor schema <code>sal init --salmodule</code> creates.
            </li>
            <li>
              Describe the module, bind the prefixes your shapes use, and describe each task: what configures it, and
              the shape of what it prints. The preview on the right is exactly what will be written, checked as you type
              with the same rules <code>sal validate --salmodule</code> applies.
            </li>
            <li>
              Write is enabled once the document has no problems and differs from the file. A file edited by hand while
              this page is open is not written over; reload the page to pick it up. Stop the page with <kbd>Ctrl</kbd>+
              <kbd>C</kbd> in the terminal, and run <code>sal edit</code> in the module to open it again.
            </li>
            <li>
              See the{' '}
              <a href="https://cgs-earth.github.io/sal/reference/salmodule-description/" target="_blank" rel="noreferrer">
                SAL module specification
              </a>{' '}
              for what the container must do with the ontology.
            </li>
          </ul>
        </div>
      )}

      <main className="content">
        {error && <p className="error-banner">{error}</p>}
        {!error && !status && <p className="empty">Asking sal about the module…</p>}
        {!error && status && (
          // keyed on whether the file exists, so that one created or deleted
          // outside the form starts it over, while edits to it do not
          <ModuleForm key={`${status.ontologyPath}:${status.initialized}`} status={status} />
        )}
      </main>
    </div>
  )
}
