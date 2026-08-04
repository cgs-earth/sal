import { useCallback, useEffect, useState } from 'react'
import { inspectModule, type ModuleOntology } from '../api'

const EXAMPLE = 'salmodule://github.com/adplincinst/sample-salmodule-1'

type ModulesTabProps = {
  /** The modules the built table recorded, or null when the table records none. */
  modules: string[] | null
}

/** Shortens a module URI to the OWNER/REPO a chip has room for. */
function chipLabel(module: string) {
  const segments = module.replace(/^salmodule:\/\//, '').split('/').filter(Boolean)
  return segments.slice(-2).join('/') || module
}

export function ModulesTab({ modules }: ModulesTabProps) {
  const [reference, setReference] = useState('')
  const [result, setResult] = useState<ModuleOntology | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [running, setRunning] = useState(false)
  const [copied, setCopied] = useState(false)

  const tracked = modules ?? []
  // a table built without modules still gets one thing to click, so the tab is
  // never an empty box with no idea of what belongs in it
  const suggestions = tracked.length > 0 ? tracked : [EXAMPLE]

  const inspect = useCallback(async (text: string) => {
    if (!text.trim()) {
      setError('Enter a SAL module reference')
      return
    }
    setRunning(true)
    setError(null)
    try {
      setResult(await inspectModule(text.trim()))
    } catch (caught) {
      setResult(null)
      setError(caught instanceof Error ? caught.message : String(caught))
    } finally {
      setRunning(false)
    }
  }, [])

  useEffect(() => {
    if (!copied) return
    const timer = setTimeout(() => setCopied(false), 1200)
    return () => clearTimeout(timer)
  }, [copied])

  // the pretty printed JSON is what the panel shows, so that is what gets copied
  const copyJSON = async (ontology: unknown) => {
    await navigator.clipboard.writeText(JSON.stringify(ontology, null, 2))
    setCopied(true)
  }

  return (
    <div className="tab-body module">
      <section className="panel">
        <header className="panel-header">
          <h3>Inspect a SAL module</h3>
          <p>
            Clones the module, builds its <code>Dockerfile</code>, and runs its <code>ontology</code> command.
          </p>
        </header>
        <div className="chips">
          {tracked.length > 0 && <span className="chips-label">In this table</span>}
          {suggestions.map((module) => (
            <button
              key={module}
              type="button"
              className="chip"
              title={module}
              onClick={() => setReference(module)}
            >
              {tracked.length > 0 ? chipLabel(module) : 'Example'}
            </button>
          ))}
        </div>
        <form
          className="module-form"
          onSubmit={(event) => {
            event.preventDefault()
            void inspect(reference)
          }}
        >
          <input
            className="module-input"
            type="text"
            value={reference}
            spellCheck={false}
            placeholder={suggestions[0]}
            aria-label="SAL module reference"
            list="salmodule-suggestions"
            onChange={(event) => setReference(event.target.value)}
          />
          {/* the modules the table was built from autocomplete as the reference is typed */}
          <datalist id="salmodule-suggestions">
            {tracked.map((module) => (
              <option key={module} value={module} />
            ))}
          </datalist>
          <button type="submit" className="button primary" disabled={running}>
            {running ? 'Inspecting…' : 'Inspect'}
          </button>
        </form>
        <p className="hint module-hint">
          The <code>salmodule://</code> scheme is optional; <code>OWNER/REPO</code> is enough for a module on
          GitHub. The first inspection of a module builds its image, which can take a few minutes.
          {tracked.length > 0 && ' The suggestions above are the modules recorded in this table’s Iceberg metadata.'}
        </p>
      </section>

      <section className="panel results-panel">
        <header className="panel-header">
          <h3>Ontology</h3>
          <div className="panel-header-actions">
            {result && <span className="badge">{result.module}</span>}
            {result && (
              <button
                type="button"
                className={copied ? 'icon-button copied' : 'icon-button'}
                title={copied ? 'Copied' : 'Copy the ontology JSON'}
                aria-label={copied ? 'Copied' : 'Copy the ontology JSON'}
                onClick={() => void copyJSON(result.ontology)}
              >
                {copied ? <CheckIcon /> : <ClipboardIcon />}
              </button>
            )}
          </div>
        </header>
        {error && <p className="error-banner">{error}</p>}
        {!error && running && <p className="empty">Cloning and building the module…</p>}
        {!error && !running && !result && <p className="empty">Inspect a module to see its ontology here.</p>}
        {!error && !running && result && (
          <div className="json-scroll">
            <pre className="json-block">{JSON.stringify(result.ontology, null, 2)}</pre>
          </div>
        )}
      </section>
    </div>
  )
}

function ClipboardIcon() {
  return (
    <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
      <rect x="5.25" y="1.75" width="5.5" height="2.5" rx="0.75" />
      <path d="M10.75 3h1.5c.55 0 1 .45 1 1v9.25c0 .55-.45 1-1 1H3.75c-.55 0-1-.45-1-1V4c0-.55.45-1 1-1h1.5" />
    </svg>
  )
}

function CheckIcon() {
  return (
    <svg
      viewBox="0 0 16 16"
      width="14"
      height="14"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M3.5 8.5 6.5 11.5 12.5 4.5" />
    </svg>
  )
}
