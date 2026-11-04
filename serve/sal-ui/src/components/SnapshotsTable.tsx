import { CopyButton } from './CopyButton'

type SnapshotsTableProps = {
  /** The `sal query --info snapshots` result: a header naming the columns and a row per snapshot, newest first. */
  header: string[] | null
  rows: string[][] | null
}

/** One row of the listing, read from the result by column name so the order the SQL projects does not matter here. */
type Snapshot = {
  id: string
  timestamp: string
  message: string
  added: number
  removed: number
  gitCommits: string[]
  metadataFile: string
}

const formatCount = (value: number) => new Intl.NumberFormat().format(value)

/**
 * The Stats tab's snapshot listing: every commit to the Iceberg table with
 * what it changed in triples, the message the build was given, the git
 * commits the build tagged it with, and the metadata file it was committed in,
 * which the copy button puts on the clipboard as the full path DuckDB's
 * iceberg_scan takes.
 */
export function SnapshotsTable({ header, rows }: SnapshotsTableProps) {
  const snapshots = readSnapshots(header, rows)
  if (snapshots.length === 0) {
    return <p className="empty">No snapshots yet; run sal build to commit one</p>
  }
  return (
    <div className="table-scroll">
      <table className="result-table snapshots-table">
        <thead>
          <tr>
            <th scope="col">Snapshot</th>
            <th scope="col">Timestamp</th>
            <th scope="col">Message</th>
            <th scope="col">Triples</th>
            <th scope="col">Git commit</th>
            <th scope="col">Metadata file</th>
          </tr>
        </thead>
        <tbody>
          {snapshots.map((snapshot) => (
            <tr key={snapshot.id}>
              <td title={snapshot.id}>
                <code>{snapshot.id}</code>
              </td>
              <td title={snapshot.timestamp}>{toMinute(snapshot.timestamp) || <span className="null">∅</span>}</td>
              <td className="snapshot-message" title={snapshot.message}>
                {snapshot.message || <span className="null">∅</span>}
              </td>
              <td className="snapshot-triples" title={`${formatCount(snapshot.added)} added, ${formatCount(snapshot.removed)} removed`}>
                <span className="triples-added">+{formatCount(snapshot.added)}</span>{' '}
                <span className={snapshot.removed > 0 ? 'triples-removed' : 'null'}>−{formatCount(snapshot.removed)}</span>
              </td>
              <td title={snapshot.gitCommits.join(', ')}>
                {snapshot.gitCommits.length === 0 ? (
                  <span className="null">∅</span>
                ) : (
                  // a snapshot can carry several commits, since a build that
                  // changes nothing tags the snapshot it already has; each is
                  // its whole hash on a line of its own
                  snapshot.gitCommits.map((commit) => (
                    <code key={commit} className="git-commit">
                      {commit}
                    </code>
                  ))
                )}
              </td>
              <td className="snapshot-metadata" title={snapshot.metadataFile}>
                {snapshot.metadataFile ? (
                  <>
                    <code>{fileName(snapshot.metadataFile)}</code>
                    <CopyButton text={snapshot.metadataFile} label="Copy the metadata file's full path" />
                  </>
                ) : (
                  <span className="null">∅</span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/** Picks the listing's columns out of the result by name; a column the server does not send reads as empty. */
function readSnapshots(header: string[] | null, rows: string[][] | null): Snapshot[] {
  if (!header || !rows) return []
  const column = (name: string) => header.indexOf(name)
  const at = (row: string[], name: string) => {
    const index = column(name)
    return index === -1 ? '' : (row[index] ?? '')
  }
  return rows.map((row) => ({
    id: at(row, 'snapshot_id'),
    timestamp: at(row, 'timestamp_ms'),
    message: at(row, 'message'),
    added: Number(at(row, 'triples_added')) || 0,
    removed: Number(at(row, 'triples_removed')) || 0,
    gitCommits: at(row, 'git_commits')
      .split(',')
      .map((commit) => commit.trim())
      .filter((commit) => commit !== ''),
    metadataFile: at(row, 'metadata_file'),
  }))
}

/**
 * A DuckDB timestamp, `2026-11-04 16:28:27.093`, to the minute, `2026-11-04 16:28`;
 * the full value stays in the cell's tooltip. Anything else is shown as it is.
 */
function toMinute(timestamp: string) {
  const match = /^(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2})/.exec(timestamp)
  return match ? `${match[1]} ${match[2]}` : timestamp
}

/** The last segment of a path or URL, `v12.metadata.json`. */
function fileName(path: string) {
  const segments = path.replace(/\/+$/, '').split('/')
  return segments[segments.length - 1] || path
}
