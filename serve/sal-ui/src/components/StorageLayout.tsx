import { useMemo, useState } from 'react'
import type { DataFile, QueryResult, TableLayout } from '../api'
import { fileName, formatBytes, formatCount, formatShare, layoutNotes, pruningVerdict } from './layoutSummary'

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

      {notes.length > 0 && (
        <ul className="layout-notes">
          {notes.map((note) => (
            <li key={note.text} className={`layout-note ${note.tone}`}>
              {note.text}
            </li>
          ))}
        </ul>
      )}

      <div className="table-scroll layout-pruning">
        <table className="result-table pruning-table">
          <thead>
            <tr>
              <th scope="col">Column</th>
              <th
                scope="col"
                title="The most data files a scan filtered to one value of this column has to read: every file whose recorded lowest-to-highest range admits that value. A scan skips the other files by their bounds without opening them."
              >
                Worst-case files read per scan
              </th>
              <th scope="col" title="The share of the rows in which this column is null">
                Null share
              </th>
              <th
                scope="col"
                title="How well a filter on this column skips files: good when one value falls in few files, poor when the files' ranges overlap so much that most are read. Hover a grade for the detail."
              >
                Filter performance
              </th>
            </tr>
          </thead>
          <tbody>
            {columns.map((column) => {
              const verdict = pruningVerdict(column, data.count)
              return (
                <tr key={column.name}>
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
                    {column.filesWithBounds === 0 || column.maxFilesPerValue === 0
                      ? '—'
                      : `${formatCount(column.maxFilesPerValue)} of ${formatCount(column.filesWithBounds)}`}
                  </td>
                  <td>{formatShare(column.nulls, data.rows)}</td>
                  <td className={`layout-verdict ${verdict.tone}`} title={verdict.detail}>
                    {verdict.grade}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
        {columns.length === 0 && <p className="empty">No column statistics recorded</p>}
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
