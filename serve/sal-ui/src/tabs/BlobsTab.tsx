import { useCallback, useState } from 'react'
import { resolveBlob } from '../api'

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

export function BlobsTab() {
  const [hash, setHash] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [downloading, setDownloading] = useState(false)

  const download = useCallback(async (text: string) => {
    setDownloading(true)
    setError(null)
    try {
      const resolved = await resolveBlob(text)
      saveAs(resolved.blob, resolved.digest)
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught))
    } finally {
      setDownloading(false)
    }
  }, [])

  return (
    <div className="tab-body blob">
      <section className="panel">
        <header className="panel-header">
          <h3>Download a pinned blob</h3>
          <p>
            Fetches a pinned blob such as a vocabulary or imported ontology document contained in the SAL project.
          </p>
        </header>
        <form
          className="module-form"
          onSubmit={(event) => {
            event.preventDefault()
            void download(hash)
          }}
        >
          <input
            className="module-input"
            type="text"
            value={hash}
            spellCheck={false}
            placeholder={EXAMPLE}
            aria-label="Blob SHA-256 digest"
            onChange={(event) => setHash(event.target.value)}
          />
          <button type="submit" className="button primary" disabled={downloading}>
            {downloading ? 'Downloading…' : 'Download'}
          </button>
        </form>
        {error && <p className="error-banner">{error}</p>}
        <p className="hint module-hint">
          Paste the SHA-256 digest a document is pinned at, either bare or as <code>urn:sha256:…</code> — the
          prefix is stripped either way. A digest with no matching document answers a 404.
        </p>
      </section>
    </div>
  )
}
