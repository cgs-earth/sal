import { useEffect, useState } from 'react'
import { ThemeToggle } from '../components/ThemeToggle'
import { fetchInitStatus, type InitStatus } from './api'
import { ModuleForm } from './ModuleForm'
import '../App.css'
import './init.css'

/** How often the module root is checked for ontology.jsonld appearing or disappearing. */
const STATUS_POLL_MS = 2000

/**
 * The page `sal init --salmodule --ui` opens: the form that writes a module's
 * ontology.jsonld while it does not exist, and a notice that the module is
 * initialized once it does. The server reads the disk on every status
 * request, so deleting the file unlocks the form again, and writing it from
 * the form locks it.
 */
export function InitApp() {
  const [status, setStatus] = useState<InitStatus | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [showHelp, setShowHelp] = useState(false)

  useEffect(() => {
    const controller = new AbortController()
    const load = async () => {
      try {
        setStatus(await fetchInitStatus(controller.signal))
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
          <span className="brand-sub">module init</span>
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
              declaring the tasks a project can run. This page writes the <code>ontology.jsonld</code>, with the
              Dockerfile and editor schema <code>sal init --salmodule</code> scaffolds beside it.
            </li>
            <li>
              Fill in the module, bind the prefixes your shapes use, and describe each task. The preview on the right is
              exactly what will be written, checked as you type with the same rules{' '}
              <code>sal validate --salmodule</code> applies.
            </li>
            <li>
              Write is enabled once the document has no problems. The file is written once: to start over, delete{' '}
              <code>ontology.jsonld</code> and this page unlocks again. Stop the page with <kbd>Ctrl</kbd>+<kbd>C</kbd> in
              the terminal.
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
        {!error && status && status.initialized && <Initialized status={status} />}
        {!error && status && !status.initialized && (
          // keyed on the path so that a module unlocked by deleting its
          // ontology starts from a fresh form rather than the one written
          <ModuleForm key={status.ontologyPath} status={status} />
        )}
      </main>
    </div>
  )
}

function Initialized({ status }: { status: InitStatus }) {
  return (
    <div className="tab-body init">
      <section className="panel init-done">
        <header className="panel-header">
          <h3>Module initialized</h3>
          <span className="badge">{status.namespace}</span>
        </header>
        <div className="init-done-body">
          <p>
            <code>{status.ontologyPath}</code> exists, so this page will not write another ontology. The form edits
            nothing yet: to change the ontology, edit the file in your editor, where the schema sal registered in{' '}
            <code>.vscode/settings.json</code> checks it as you type, and run <code>sal validate --salmodule</code>.
          </p>
          <p>To start over, delete the file and this page unlocks again.</p>
          <p className="hint">
            Next: write the <code>Dockerfile</code> so the container answers <code>salmodule ontology</code> with this
            document and <code>salmodule run</code> with newline delimited JSON, then press <kbd>Ctrl</kbd>+<kbd>C</kbd>{' '}
            in the terminal to stop this page.
          </p>
        </div>
      </section>
    </div>
  )
}
