import { useCallback, useEffect, useState } from 'react'
import { resolveBlob } from '../api'
import { blobLink } from '../routing'

const EXAMPLE = '9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a0'

/** Saves blob to disk under the browser's normal download flow. */
function saveAs(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  URL.revokeObjectURL(url)
}

/** A blob read for display: what the tab shows when asked to render rather than download. */
type Rendered = {
  digest: string
  version: string
  blob: Blob
  /** The blob's text, pretty printed when it is JSON, or null when it is not text at all. */
  text: string | null
}

/**
 * Reads blob as text for display. The server answers every blob as
 * application/octet-stream, so what it is has to be judged from the bytes: a
 * NUL byte means it is not a document that can be shown, and a body that
 * parses as JSON (a JSON-LD ontology, say) is reformatted for reading.
 */
async function readForDisplay(blob: Blob): Promise<string | null> {
  const text = await blob.text()
  if (text.includes('\0')) return null
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return text
  }
}

type BlobsTabProps = {
  /** The digest the tab was opened on, from the `hash` parameter, or null. */
  hash: string | null
  /** Whether the tab was opened with `render=true`, asking for the blob in the page. */
  render: boolean
}

export function BlobsTab({ hash: initialHash, render: initialRender }: BlobsTabProps) {
  const [hash, setHash] = useState(initialHash ?? '')
  const [render, setRender] = useState(initialRender)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [rendered, setRendered] = useState<Rendered | null>(null)

  // The address bar mirrors the form, so the URL can be copied at any point and
  // reopen the tab on the same digest with the same choice. replaceState keeps
  // typing a digest from filling the browser history.
  useEffect(() => {
    const url = blobLink(hash.trim(), render)
    if (`${window.location.pathname}${window.location.search}` !== url) {
      window.history.replaceState(null, '', url)
    }
  }, [hash, render])

  const resolve = useCallback(async (text: string, show: boolean) => {
    setBusy(true)
    setError(null)
    try {
      const resolved = await resolveBlob(text)
      if (show) {
        setRendered({
          digest: resolved.digest,
          version: resolved.version,
          blob: resolved.blob,
          text: await readForDisplay(resolved.blob),
        })
      } else {
        saveAs(resolved.blob, resolved.digest)
      }
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught))
    } finally {
      setBusy(false)
    }
  }, [])

  // A link carrying render=true, such as the ones on the Stats tab's ontology
  // listing, shows the blob straight away. A link without it only fills the
  // form in: a download is left for the user to start.
  useEffect(() => {
    if (initialHash && initialRender) void resolve(initialHash, true)
  }, [initialHash, initialRender, resolve])

  return (
    <div className="tab-body blob">
      <section className="panel">
        <header className="panel-header">
          <h3>{render ? 'Render a pinned blob' : 'Download a pinned blob'}</h3>
          <p>
            Fetches a pinned blob such as a vocabulary, imported ontology, or SAL module ontology document contained in
            the SAL project.
          </p>
        </header>
        <form
          className="module-form"
          onSubmit={(event) => {
            event.preventDefault()
            void resolve(hash, render)
          }}
        >
          <input
            className="module-input"
            type="text"
            value={hash}
            spellCheck={false}
            placeholder={EXAMPLE}
            aria-label="Blob SHA-256 digest or git commit hash"
            onChange={(event) => setHash(event.target.value)}
          />
          <button type="submit" className="button primary" disabled={busy}>
            {busy ? 'Fetching…' : render ? 'Render' : 'Download'}
          </button>
        </form>
        <label className="toggle blob-toggle">
          <input type="checkbox" checked={render} onChange={(event) => setRender(event.target.checked)} />
          Render in the browser instead of downloading (<code>?render=true</code>)
        </label>
        {error && <p className="error-banner">{error}</p>}
        <p className="hint module-hint">
          Paste the SHA-256 digest a document is pinned at. Arbitrary blobs use the form <code>urn:sha256:...</code>. Salmodules use <code>urn:git-commit-hash:…</code>. You can also specify the hash directly without the prefix. A hash with no matching document
          answers a 404.
        </p>
      </section>

      {rendered && (
        <section className="panel">
          <header className="panel-header blob-view-header">
            <div>
              <h3>
                <code>{rendered.version}</code>
              </h3>
              <p>{new Intl.NumberFormat().format(rendered.blob.size)} bytes</p>
            </div>
            <button type="button" className="button" onClick={() => saveAs(rendered.blob, rendered.digest)}>
              Download
            </button>
          </header>
          {rendered.text === null ? (
            <p className="empty">This blob is not text, so it cannot be shown here; download it instead.</p>
          ) : (
            <div className="json-scroll">
              <pre className="json-block">{rendered.text}</pre>
            </div>
          )}
        </section>
      )}
    </div>
  )
}
