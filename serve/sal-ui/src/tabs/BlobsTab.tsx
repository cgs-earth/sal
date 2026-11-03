import { useEffect, useMemo, useState } from 'react'
import { fetchBlobs, resolveBlob, type BlobListing, type PinnedBlob } from '../api'
import { LoadingState } from '../components/LoadingState'
import { blobLink, shareLink, visit } from '../routing'

/**
 * How many blobs the listing shows. The table can refer to far more, one row
 * per pinned vocabulary version and per file a module task handed over, and
 * past this many the query tabs are the right tool for finding one.
 */
const BLOB_LIST_LIMIT = 100

/**
 * The largest blob shown in the page. A pinned vocabulary document is a few
 * hundred kilobytes at most; anything past this is a module's data, which the
 * browser would choke on as one preformatted block, so it is offered for
 * download instead.
 */
const MAX_RENDERED_BYTES = 4 * 1024 * 1024

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

/** A blob read for display: what the blob view shows once it has been fetched. */
type Rendered = {
  digest: string
  version: string
  blob: Blob
  /** The blob's text, pretty printed when it is JSON, or null when it is not text or is too large to show. */
  text: string | null
}

/**
 * Reads blob as text for display. The server answers every blob as
 * application/octet-stream, so what it is has to be judged from the bytes: a
 * NUL byte means it is not a document that can be shown, and a body that
 * parses as JSON (a JSON-LD ontology, say) is reformatted for reading. A blob
 * past MAX_RENDERED_BYTES is not read at all.
 */
async function readForDisplay(blob: Blob): Promise<string | null> {
  if (blob.size > MAX_RENDERED_BYTES) return null
  const text = await blob.text()
  if (text.includes('\0')) return null
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return text
  }
}

/** Strips the urn:sha256: or urn:git-commit-hash: prefix so two spellings of one blob compare equal. */
function bareDigest(hash: string): string {
  return hash.trim().replace(/^urn:(sha256|git-commit-hash):/i, '')
}

type BlobsTabProps = {
  /** The digest the tab was opened on, from the `hash` parameter, which opens the blob's own view; null shows the listing. */
  hash: string | null
}

/**
 * The Blobs tab has two views, chosen by the route: the listing of every blob
 * the table refers to, and a view of one blob, opened by clicking a row or
 * following a `?hash=` link and left by its back arrow. The listing is read
 * once, when the tab opens, and kept across the two views so that going back
 * is instant, with the filter and sort as they were, and so the blob view can
 * name a blob by the IRI the listing knows it by.
 */
export function BlobsTab({ hash }: BlobsTabProps) {
  const [listing, setListing] = useState<BlobListing | null>(null)
  const [listError, setListError] = useState<string | null>(null)
  // The filter and the sort live here rather than in the listing, which is
  // unmounted while a blob is shown, so going back finds them as they were.
  const [filter, setFilter] = useState('')
  const [sort, setSort] = useState<BlobSort>({ column: 'iri', descending: false })

  useEffect(() => {
    const controller = new AbortController()
    fetchBlobs(BLOB_LIST_LIMIT, controller.signal)
      .then(setListing)
      .catch((caught: unknown) => {
        if (controller.signal.aborted) return
        setListError(caught instanceof Error ? caught.message : String(caught))
      })
    return () => controller.abort()
  }, [])

  if (hash) {
    const digest = bareDigest(hash)
    const pinned = listing?.blobs.find((blob) => bareDigest(blob.hash) === digest) ?? null
    // Keyed on the digest so that moving between two blob links fetches the
    // new one rather than showing the old one under the new heading.
    return <BlobView key={digest} hash={hash} pinned={pinned} />
  }

  return (
    <div className="tab-body blob">
      <BlobList listing={listing} error={listError} filter={filter} onFilter={setFilter} sort={sort} onSort={setSort} />
    </div>
  )
}

/** A column of the listing the rows can be sorted by. */
type BlobColumn = 'iri' | 'hash' | 'modified'

const COLUMNS: { key: BlobColumn; label: string }[] = [
  { key: 'iri', label: 'IRI' },
  { key: 'hash', label: 'hash' },
  { key: 'modified', label: 'modified' },
]

type BlobSort = { column: BlobColumn; descending: boolean }

/**
 * Orders the listing by one column. A blob with no modified time sorts after
 * every one that has it whichever way the column is sorted, since it has
 * nothing to compare.
 */
function sortBlobs(blobs: PinnedBlob[], sort: BlobSort): PinnedBlob[] {
  const direction = sort.descending ? -1 : 1
  return [...blobs].sort((a, b) => {
    const left = a[sort.column] ?? ''
    const right = b[sort.column] ?? ''
    if (left === right) return 0
    if (left === '') return 1
    if (right === '') return -1
    return direction * left.localeCompare(right)
  })
}

type BlobListProps = {
  /** The blobs the table refers to, or null while they are still being read. */
  listing: BlobListing | null
  error: string | null
  filter: string
  onFilter: (filter: string) => void
  sort: BlobSort
  onSort: (sort: BlobSort) => void
}

/**
 * Every blob the table refers to, the vocabularies the Stats tab lists and the
 * files and directories SAL module tasks handed over. Each row links to the
 * blob's own view, the way the Stats tab's version links do. The filter in the
 * header keeps the rows whose IRI or hash contains what is typed, ignoring
 * case, and clicking a column heading sorts by it, again to reverse; both act
 * on the rows the listing holds, which are the first BLOB_LIST_LIMIT the
 * table refers to.
 */
function BlobList({ listing, error, filter, onFilter, sort, onSort }: BlobListProps) {
  const rows = useMemo(() => {
    if (!listing) return []
    const needle = filter.trim().toLowerCase()
    const kept = needle
      ? listing.blobs.filter((blob) => blob.iri.toLowerCase().includes(needle) || blob.hash.toLowerCase().includes(needle))
      : listing.blobs
    return sortBlobs(kept, sort)
  }, [listing, filter, sort])

  const count = listing?.blobs.length ?? 0
  // the noun agrees with how many are shown: "1 file", "12 files", "2 of 12 files"
  const files = (n: number) => (n === 1 ? 'file' : 'files')
  let summary = 'Reading the table…'
  if (error) summary = 'Could not be read'
  else if (listing && filter.trim()) summary = `Showing ${rows.length} of ${count} ${files(count)}`
  else if (listing && listing.truncated) summary = `Showing the first ${count} ${files(count)} of more`
  else if (listing) summary = `Showing ${count} ${files(count)}`

  const sortBy = (column: BlobColumn) => onSort({ column, descending: sort.column === column && !sort.descending })

  return (
    <section className="panel">
      <header className="panel-header">
        <h3>Pinned blobs</h3>
        <p>{summary}</p>
        <div className="panel-header-actions">
          <input
            className="module-input blob-filter"
            type="search"
            value={filter}
            spellCheck={false}
            placeholder="Filter by IRI or hash"
            aria-label="Filter the blobs by IRI or hash"
            disabled={!listing || listing.blobs.length === 0}
            onChange={(event) => onFilter(event.target.value)}
          />
        </div>
      </header>
      <p className="hint module-hint blob-list-hint">
        Every pinned blob contained within the SAL project. May include either pinned vocabularies or files copied
        in by a salmodule task.
      </p>
      {error && <p className="error-banner">{error}</p>}
      {listing && listing.blobs.length === 0 && (
        <p className="empty">The table refers to no blobs yet; run sal build to pin the vocabularies it resolves against.</p>
      )}
      {listing && listing.blobs.length > 0 && rows.length === 0 && (
        <p className="empty">No blob's IRI or hash contains “{filter.trim()}”.</p>
      )}
      {rows.length > 0 && (
        <div className="table-scroll blob-list-scroll">
          <table className="result-table">
            <thead>
              <tr>
                {COLUMNS.map(({ key, label }) => (
                  <th
                    key={key}
                    scope="col"
                    className="blob-sortable"
                    aria-sort={sort.column === key ? (sort.descending ? 'descending' : 'ascending') : 'none'}
                  >
                    <button type="button" className="blob-sort" onClick={() => sortBy(key)}>
                      {label}
                      <span className="blob-sort-mark" aria-hidden="true">
                        {sort.column === key ? (sort.descending ? '▼' : '▲') : ''}
                      </span>
                    </button>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((blob) => (
                <tr key={`${blob.iri}|${blob.hash}`}>
                  <td title={blob.iri}>
                    <BlobRowLink hash={blob.hash}>{blob.iri}</BlobRowLink>
                  </td>
                  <td title={blob.hash}>
                    <BlobRowLink hash={blob.hash}>{blob.hash}</BlobRowLink>
                  </td>
                  <td className="blob-modified">{blob.modified ? <time dateTime={blob.modified}>{blob.modified}</time> : <span className="null">∅</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {listing?.truncated && (
        <p className="hint module-hint blob-list-hint">
          Only the first {BLOB_LIST_LIMIT} blobs are listed, filtered, and sorted here. To look up the rest, query{' '}
          <code>owl:versionIRI</code> in the SPARQL tab or{' '}
          <a
            href={shareLink('SQL', listing.sql)}
            onClick={(event) => {
              event.preventDefault()
              visit(shareLink('SQL', listing.sql))
            }}
          >
            run this listing's SQL in the SQL tab
          </a>{' '}
          with a higher limit.
        </p>
      )}
    </section>
  )
}

/**
 * A link in the listing to a blob's view. It is a real link to the view's URL,
 * so it can be opened in a new tab or copied, and in place it is followed
 * through the router so the back arrow and the browser's back button both
 * return to the listing.
 */
function BlobRowLink({ hash, children }: { hash: string; children: string }) {
  const href = blobLink(hash)
  return (
    <a
      href={href}
      className="iri"
      title="Open this blob"
      onClick={(event) => {
        event.preventDefault()
        visit(href)
      }}
    >
      {children}
    </a>
  )
}

type BlobViewProps = {
  /** The hash the view was opened on, as the route carries it. */
  hash: string
  /** The listing's entry for the blob, which names it by its IRI, or null when the listing does not have it. */
  pinned: PinnedBlob | null
}

/**
 * One blob, fetched when the view opens: its text in a scrolling block when it
 * is a document, or a note when it is not. The back arrow returns to the
 * listing; of the two icon buttons at the far end, the first puts the text
 * shown on the clipboard, for a blob that is text, and the second saves any
 * blob through the browser's download flow.
 */
function BlobView({ hash, pinned }: BlobViewProps) {
  const [rendered, setRendered] = useState<Rendered | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    if (!copied) return
    const timer = setTimeout(() => setCopied(false), 1600)
    return () => clearTimeout(timer)
  }, [copied])

  useEffect(() => {
    const controller = new AbortController()
    resolveBlob(hash, controller.signal)
      .then(async (resolved) =>
        setRendered({
          digest: resolved.digest,
          version: resolved.version,
          blob: resolved.blob,
          text: await readForDisplay(resolved.blob),
        }),
      )
      .catch((caught: unknown) => {
        if (controller.signal.aborted) return
        setError(caught instanceof Error ? caught.message : String(caught))
      })
    return () => controller.abort()
  }, [hash])

  const back = () => visit(blobLink(''))
  const title = pinned?.iri ?? rendered?.version ?? hash

  return (
    <div className="tab-body blob-view">
      <section className="panel">
        <header className="panel-header blob-view-header">
          <button type="button" className="icon-button blob-back" aria-label="Back to the pinned blobs" title="Back to the pinned blobs" onClick={back}>
            <BackIcon />
          </button>
          <div className="blob-view-title">
            <h3>
              <code>{title}</code>
            </h3>
            <p>
              {pinned && <code>{rendered?.version ?? pinned.hash}</code>}
              {pinned && rendered && ' · '}
              {rendered && `${new Intl.NumberFormat().format(rendered.blob.size)} bytes`}
              {pinned?.modified && (
                <>
                  {' · modified '}
                  <time dateTime={pinned.modified}>{pinned.modified}</time>
                </>
              )}
            </p>
          </div>
          <div className="panel-header-actions">
            <button
              type="button"
              className={copied ? 'icon-button blob-action copied' : 'icon-button blob-action'}
              title={copied ? 'Copied' : 'Copy the text to the clipboard'}
              aria-label={copied ? 'Copied' : 'Copy the text to the clipboard'}
              disabled={!rendered || rendered.text === null}
              onClick={() => {
                if (!rendered || rendered.text === null) return
                void navigator.clipboard.writeText(rendered.text).then(() => setCopied(true))
              }}
            >
              {copied ? <CheckIcon /> : <ClipboardIcon />}
            </button>
            <button
              type="button"
              className="icon-button blob-action"
              title="Download"
              aria-label="Download"
              disabled={!rendered}
              onClick={() => rendered && saveAs(rendered.blob, rendered.digest)}
            >
              <DownloadIcon />
            </button>
          </div>
        </header>
        {error && <p className="error-banner">{error}</p>}
        {!error && !rendered && <LoadingState message="Fetching the blob…" delay={200} />}
        {rendered && rendered.text === null && (
          <p className="empty">
            {rendered.blob.size > MAX_RENDERED_BYTES
              ? 'This blob is too large to show here; download it instead.'
              : 'This blob is not text, so it cannot be shown here; download it instead.'}
          </p>
        )}
        {rendered && rendered.text !== null && (
          <div className="json-scroll">
            <pre className="json-block">{rendered.text}</pre>
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
    <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M3.5 8.5 6.5 11.5 12.5 4.5" />
    </svg>
  )
}

function DownloadIcon() {
  return (
    <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M8 2.5v8" />
      <path d="M4.75 7.25 8 10.5l3.25-3.25" />
      <path d="M2.75 11.75v1c0 .55.45 1 1 1h8.5c.55 0 1-.45 1-1v-1" />
    </svg>
  )
}

function BackIcon() {
  return (
    <svg
      viewBox="0 0 16 16"
      width="16"
      height="16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M13 8H3" />
      <path d="M7.5 3.5 3 8l4.5 4.5" />
    </svg>
  )
}
