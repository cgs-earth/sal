import { blobLink, visit } from '../routing'

type ResultTableProps = {
  header: string[] | null
  rows: string[][] | null
  empty?: string
}

/** Renders a DuckDB result set as a scrollable table with a sticky header. */
export function ResultTable({ header, rows, empty = 'No rows' }: ResultTableProps) {
  const columns = header ?? []
  const records = rows ?? []

  if (columns.length === 0) {
    return <p className="empty">{empty}</p>
  }

  return (
    <div className="table-scroll">
      <table className="result-table">
        <thead>
          <tr>
            <th className="row-number" scope="col">
              #
            </th>
            {columns.map((name, index) => (
              <th key={`${name}-${index}`} scope="col">
                {name}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {records.map((row, rowIndex) => (
            <tr key={rowIndex}>
              <td className="row-number">{rowIndex + 1}</td>
              {columns.map((_, columnIndex) => {
                const value = row[columnIndex] ?? ''
                return (
                  <td key={columnIndex} title={value}>
                    <Cell value={value} />
                  </td>
                )
              })}
            </tr>
          ))}
        </tbody>
      </table>
      {records.length === 0 && <p className="empty">{empty}</p>}
    </div>
  )
}

/** Renders IRIs as links and everything else as plain text. */
function Cell({ value }: { value: string }) {
  if (value === '') return <span className="null">∅</span>
  if (value.startsWith('http://') || value.startsWith('https://')) {
    return (
      <a href={value} target="_blank" rel="noreferrer" className="iri">
        {value}
      </a>
    )
  }
  // A urn:sha256: or urn:git-commit-hash: IRI is how a pinned document is
  // named, both in the ontology listing's version column and in the
  // owl:versionIRI provenance a build commits, so it opens the blob itself,
  // rendered rather than downloaded since a pinned document is meant to be read.
  if (/^urn:(sha256|git-commit-hash):/i.test(value)) {
    const href = blobLink(value, true)
    return (
      <a
        href={href}
        className="iri"
        title="Open this pinned document in the Blobs tab"
        onClick={(event) => {
          event.preventDefault()
          visit(href)
        }}
      >
        {value}
      </a>
    )
  }
  // salmodule://, oci://, and other urn: IRIs have nowhere to link to, but
  // should still read as IRIs rather than literals.
  if (value.startsWith('urn:') || /^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//.test(value)) {
    return <span className="iri">{value}</span>
  }
  return <>{value}</>
}
