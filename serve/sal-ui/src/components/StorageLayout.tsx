import { useMemo, useState } from 'react'
import type { ColumnPruning, DataFile, QueryResult, TableLayout } from '../api'
import { fileName, formatBytes, formatCount, formatShare, layoutNotes, pruningVerdict, type Note } from './layoutSummary'

type StorageLayoutProps = {
  layout: TableLayout | null
  error?: string
  tablePath: string
  /** The Snapshots table's listing, which names the build each file's sequence number came from. */
  snapshots: QueryResult
}

/**
 * The storage layout of the table: the figures a query planner cares about,
 * how it is partitioned, what stands out, how well a filter on each column
 * can skip files, and under all of that the list of every file.
 */
export function StorageLayout({ layout, error, tablePath, snapshots }: StorageLayoutProps) {
  // the column whose filter performance is opened beside the table
  const [selected, setSelected] = useState<string | null>(null)
  if (!layout) {
    return <p className="empty">{error ? `The layout could not be read: ${error}` : 'No layout to show'}</p>
  }
  const data = layout.dataFiles
  const deletes = layout.deleteFiles
  const files = layout.files ?? []
  const columns = layout.columns ?? []
  const outliers = layout.outliers ?? []
  const partitioning = layout.partitioning ?? []
  const sortOrder = layout.sortOrder ?? []
  // what each column does for the layout, shown beside its name
  const partitionedBy = new Map(partitioning.map((field) => [field.column, field.transform]))
  const sortedBy = new Map(sortOrder.map((entry) => entry.split(' ') as [string, string]))
  const notes = layoutNotes(layout, outliers)
  const builds = snapshotsBySequence(snapshots)
  const selectedColumn = columns.find((column) => column.name === selected) ?? null

  return (
    <div className="layout">
      <dl className="layout-figures">
        <div className="figure">
          <dd>{formatCount(data.count)}</dd>
          <dt>Data files</dt>
        </div>
        <div className="figure">
          <dd>{formatCount(data.averageRows)}</dd>
          <dt>Average rows per file</dt>
        </div>
        <div className="figure">
          <dd>{formatCount(data.medianRows)}</dd>
          <dt>Median rows per file</dt>
        </div>
        <div className="figure">
          <dd>{formatBytes(data.bytes)}</dd>
          <dt>Data on disk</dt>
        </div>
        <div className="figure">
          <dd>{formatCount(layout.partitions)}</dd>
          <dt>Partitions</dt>
        </div>
        <div className="figure">
          <dd>{formatCount(deletes.count)}</dd>
          <dt>Delete files</dt>
        </div>
      </dl>

      <div className="layout-pruning-row">
      <div className="table-scroll layout-pruning">
        <table className="result-table pruning-table">
          <thead>
            <tr>
              <th scope="col">Column</th>
              <th
                scope="col"
                title="The most data files a scan filtered to one value of this column has to read: every file whose recorded lowest-to-highest range admits that value, or for a geometry every file whose bounding box a query can meet plus those recording no box and no null count. A scan skips the other files by their bounds without opening them."
              >
                Worst-case files read per scan
              </th>
              <th scope="col" title="The share of the rows in which this column is null">
                Null share
              </th>
              <th
                scope="col"
                title="How well a filter on this column skips files: good when one value falls in few files, investigate when the files' ranges overlap so much that most are read, which matters only when queries filter on it. Hover a grade for the detail."
              >
                Filter performance
              </th>
            </tr>
          </thead>
          <tbody>
            {columns.map((column) => {
              const verdict = pruningVerdict(column, data.count)
              const open = column.name === selected
              return (
                <tr
                  key={column.name}
                  className={`pruning-row${open ? ' selected' : ''}`}
                  aria-selected={open}
                  title="Click for what a filter on this column performs like"
                  onClick={() => setSelected(open ? null : column.name)}
                >
                  <td>
                    <code>{column.name}</code>
                    {partitionedBy.has(column.name) && (
                      <span className="layout-role" title="The table is partitioned by this column">
                        partition {partitionedBy.get(column.name)}
                      </span>
                    )}
                    {sortedBy.has(column.name) && (
                      <span className="layout-role" title="Each file is sorted by this column">
                        sort {sortedBy.get(column.name)}
                      </span>
                    )}
                  </td>
                  <td title={column.filesWithBounds > 0 ? `${column.lower} … ${column.upper}` : undefined}>
                    {column.maxFilesPerValue === 0
                      ? '—'
                      : `${formatCount(column.maxFilesPerValue)} of ${formatCount(column.type.toUpperCase().startsWith('GEOMETRY') ? data.count : column.filesWithBounds)}`}
                  </td>
                  <td>{formatShare(column.nulls, data.rows)}</td>
                  <td className={`layout-verdict ${verdict.tone}`}>
                    <button type="button" className="layout-grade">
                      {verdict.grade}
                    </button>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
        {columns.length === 0 && <p className="empty">No column statistics recorded</p>}
      </div>
      {selectedColumn ? (
        <ColumnDetail
          column={selectedColumn}
          dataFiles={data.count}
          rows={data.rows}
          partitionTransform={partitionedBy.get(selectedColumn.name)}
          sortDirection={sortedBy.get(selectedColumn.name)}
          onClose={() => setSelected(null)}
        />
      ) : (
        <Overview notes={notes} />
      )}
      </div>

      <FileList files={files} tablePath={tablePath} builds={builds} />
    </div>
  )
}

type Snapshot = { id: string; message: string }

/** The snapshot of each sequence number, with its build message, read from the Snapshots listing by column name. */
function snapshotsBySequence(snapshots: QueryResult) {
  const header = snapshots.header ?? []
  const sequence = header.indexOf('sequence_number')
  const id = header.indexOf('snapshot_id')
  const message = header.indexOf('message')
  const bySequence = new Map<number, Snapshot>()
  if (sequence < 0 || id < 0) return bySequence
  for (const row of snapshots.rows ?? []) {
    bySequence.set(Number(row[sequence]), {
      id: row[id] ?? '',
      message: message < 0 ? '' : (row[message] ?? '').split('\n')[0].trim(),
    })
  }
  return bySequence
}

/** The snapshot that wrote a file, its id with the build's message after it, or the bare sequence number when the listing lacks it. */
function SnapshotCell({ sequence, snapshot }: { sequence: number; snapshot?: Snapshot }) {
  if (!snapshot) {
    return <td title={`Iceberg snapshot sequence ${sequence}`}>sequence {sequence}</td>
  }
  return (
    <td title={`Iceberg snapshot ${snapshot.id}, sequence ${sequence}${snapshot.message ? `: ${snapshot.message}` : ''}`}>
      {snapshot.id}
      {snapshot.message && <span className="layout-build"> ({snapshot.message})</span>}
    </td>
  )
}

/** A column of the file list the rows can be sorted by. */
type FileColumn = 'partition' | 'path' | 'kind' | 'rows' | 'bytes' | 'sequenceNumber'

const FILE_COLUMNS: { key: FileColumn; label: string; numeric?: boolean }[] = [
  { key: 'partition', label: 'Partition' },
  { key: 'path', label: 'File' },
  { key: 'kind', label: 'Kind' },
  { key: 'rows', label: 'Rows', numeric: true },
  { key: 'bytes', label: 'Size', numeric: true },
  { key: 'sequenceNumber', label: 'Written by snapshot' },
]

type FileListProps = {
  files: DataFile[]
  tablePath: string
  builds: Map<number, Snapshot>
}

/**
 * Every live file of the table, open by default, sortable by any column:
 * clicking a heading sorts by it, again to reverse. The rows arrive in path
 * order, which groups them by partition.
 */
function FileList({ files, tablePath, builds }: FileListProps) {
  const [sort, setSort] = useState<{ column: FileColumn; descending: boolean }>({ column: 'path', descending: false })
  const rows = useMemo(() => {
    const direction = sort.descending ? -1 : 1
    return [...files].sort((a, b) => {
      const left = a[sort.column]
      const right = b[sort.column]
      if (typeof left === 'number' && typeof right === 'number') return (left - right) * direction
      return String(left).localeCompare(String(right)) * direction
    })
  }, [files, sort])
  const sortBy = (column: FileColumn) =>
    setSort({ column, descending: sort.column === column && !sort.descending })

  return (
    <details open className="layout-detail">
      <summary>
        <span className="layout-detail-title">File layout</span>
        <span className="layout-detail-caption">
          {formatCount(files.length)} files under <code>{tablePath}</code>
        </span>
      </summary>
      <div className="table-scroll">
        <table className="result-table files-table">
          <thead>
            <tr>
              <th className="row-number" scope="col">
                #
              </th>
              {FILE_COLUMNS.map(({ key, label, numeric }) => (
                <th
                  key={key}
                  scope="col"
                  className={`blob-sortable${numeric ? ' numeric' : ''}`}
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
            {rows.map((file, index) => (
              <tr key={file.path}>
                <td className="row-number">{index + 1}</td>
                <td title={file.partition}>{file.partition || <span className="null">∅</span>}</td>
                <td title={file.path}>{fileName(file)}</td>
                <td>{file.kind}</td>
                <td className="numeric">{formatCount(file.rows)}</td>
                <td className="numeric">{formatBytes(file.bytes)}</td>
                <SnapshotCell sequence={file.sequenceNumber} snapshot={builds.get(file.sequenceNumber)} />
              </tr>
            ))}
          </tbody>
        </table>
        {files.length === 0 && <p className="empty">No files</p>}
      </div>
    </details>
  )
}

/**
 * The pane beside the table before a column is chosen: what stands out
 * about the files, and how to get the detail of a column.
 */
function Overview({ notes }: { notes: Note[] }) {
  return (
    <aside className="column-detail layout-overview" aria-label="Overview of the layout">
      <header className="column-detail-header">
        <h4>Overview</h4>
      </header>
      {notes.length > 0 ? (
        <ul className="layout-notes">
          {notes.map((note) => (
            <li key={note.text} className={`layout-note ${note.tone}`}>
              {note.text}
            </li>
          ))}
        </ul>
      ) : (
        <p className="column-detail-text">Nothing stands out: the files are evenly sized and no delete file is applied.</p>
      )}
      <p className="column-detail-hint">Click a column for what a filter on it performs like.</p>
    </aside>
  )
}

type ColumnDetailProps = {
  column: ColumnPruning
  dataFiles: number
  rows: number
  partitionTransform?: string
  sortDirection?: string
  onClose: () => void
}

/**
 * What a filter on the selected column performs like, beside the table: the
 * grade, how many of the files a scan reads at worst as a bar, the reasoning,
 * the range the files record, and a query shape that filter takes.
 */
function ColumnDetail({ column, dataFiles, rows, partitionTransform, sortDirection, onClose }: ColumnDetailProps) {
  const verdict = pruningVerdict(column, dataFiles)
  const geometry = column.type.toUpperCase().startsWith('GEOMETRY')
  const worst = column.maxFilesPerValue
  const share = dataFiles > 0 ? worst / dataFiles : 0
  return (
    <aside className="column-detail" aria-label={`Filter performance of ${column.name}`}>
      <header className="column-detail-header">
        <h4>
          <code>{column.name}</code>
        </h4>
        <span className={`layout-verdict column-detail-grade ${verdict.tone}`}>{verdict.grade}</span>
        <button type="button" className="icon-button column-detail-close" aria-label="Close" onClick={onClose}>
          ×
        </button>
      </header>
      {worst > 0 ? (
        <>
          <p className="column-detail-headline">
            A scan filtered on this column reads at worst <strong>{formatCount(worst)}</strong> of{' '}
            <strong>{formatCount(dataFiles)}</strong> data files
          </p>
          <div
            className="column-detail-bar"
            role="img"
            aria-label={`${Math.round(share * 100)} percent of the files at worst`}
          >
            <div className={`column-detail-bar-fill ${verdict.tone}`} style={{ width: `${Math.max(2, share * 100)}%` }} />
          </div>
        </>
      ) : (
        <p className="column-detail-headline">Nothing lets a scan filtered on this column skip a file</p>
      )}
      <p className="column-detail-text">{verdict.detail}</p>
      <dl className="column-detail-facts">
        <dt>Role</dt>
        <dd>
          {partitionTransform ? `partition column, ${partitionTransform}` : ''}
          {partitionTransform && sortDirection ? '; ' : ''}
          {sortDirection ? `sort column, ${sortDirection}` : ''}
          {!partitionTransform && !sortDirection && 'neither partition nor sort column'}
        </dd>
        <dt>Files with bounds</dt>
        <dd>
          {formatCount(column.filesWithBounds)} of {formatCount(dataFiles)}
        </dd>
        <dt>Null share</dt>
        <dd>{formatShare(column.nulls, rows)}</dd>
        {column.filesWithBounds > 0 && !geometry && (
          <>
            <dt>Range over all files</dt>
            <dd>
              <code title={column.lower}>{column.lower}</code> … <code title={column.upper}>{column.upper}</code>
            </dd>
          </>
        )}
      </dl>
      <p className="column-detail-label">A filter that takes this path</p>
      <pre className="column-detail-query">{sampleFilter(column)}</pre>
    </aside>
  )
}

/** The SPARQL pattern or FILTER whose constant lands in the column, as the translator writes it. */
function sampleFilter(column: ColumnPruning): string {
  switch (column.name) {
    case 'subject':
      return '<https://example.org/thing> ?p ?o .'
    case 'predicate':
      return '?s <https://schema.org/name> ?o .'
    case 'object_iri':
      return '?s ?p <https://example.org/thing> .'
    case 'object_string':
      return '?s ?p "a literal" .'
    case 'object_language':
      return '?s ?p ?o .\nFILTER(LANG(?o) = "en")'
    case 'object_type':
      return '?s ?p ?o .\nFILTER(DATATYPE(?o) = xsd:dateTime)'
    case 'object_integer':
    case 'object_byte':
    case 'object_float':
      return '?s ?p ?o .\nFILTER(?o > 21)'
    case 'object_time':
      return '?s ?p ?o .\nFILTER(?o > "2020-01-01T00:00:00Z"^^xsd:dateTime)'
    case 'object_geometry':
      return '?s geo:asWKT ?g .\nFILTER(geof:sfIntersects(?g, "POLYGON(...)"^^geo:wktLiteral))'
    case 'triple_hash':
      return 'SELECT * FROM triples WHERE triple_hash = \'…\''
    default:
      return `?s ?p ?o .\nFILTER(?o = …)   -- ${column.name}`
  }
}
