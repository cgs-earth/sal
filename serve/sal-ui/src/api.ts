import type { Geometry } from 'geojson'

export type QueryResult = {
  sql: string
  header: string[] | null
  rows: string[][] | null
  message: string
}

export type TableStats = {
  tablePath: string
  triples: number
  subjects: number
  predicates: number
  objects: number
  snapshots: QueryResult
  properties: QueryResult
  columnStats: QueryResult
  /** The `salmodule://` URIs of the modules the build that wrote this table downloaded. */
  modules: string[] | null
  /** The same listing `sal get vocabularies` prints: header ["vocabulary", "version", "format", "imported"]. */
  vocabularies: QueryResult
  /** The imported data products, each queryable as a view of its own. */
  importedTables: ImportedTable[] | null
  /** Sample queries only the server can write, such as time travel and the import listing. */
  sampleQueries: NamedQuery[] | null
}

/** An imported SAL data product, registered as a DuckDB view named after its OCI artifact. */
export type ImportedTable = {
  view: string
  artifact: string
  path: string
}

export type NamedQuery = {
  name: string
  sql: string
}

export type ModuleOntology = {
  /** The salmodule:// namespace the module's own terms resolve against. */
  module: string
  /** The JSON-LD document the module printed in response to its ontology command. */
  ontology: unknown
}

export type BlobResult = {
  /** The name the blob was resolved by, with any urn:sha256: or urn:git-commit-hash: prefix stripped. */
  digest: string
  /** The owl:versionIRI form of digest: urn:git-commit-hash: for a 40 character git commit, urn:sha256: otherwise. */
  version: string
  blob: Blob
  contentType: string | null
}

/** A longitude/latitude bounding box as [minX, minY, maxX, maxY]. */
export type BBox = [number, number, number, number]

/** The features `/geometries` serves: one per geometry-valued object, keyed by its triple. */
export type GeoJSONFeature = {
  type: 'Feature'
  /** Set on the dataset extent only. */
  bbox?: BBox
  geometry: Geometry | null
  properties: Record<string, string>
}

export type GeoJSONFeatureCollection = {
  type: 'FeatureCollection'
  features: GeoJSONFeature[]
}

export type GeometryQuery = {
  limit?: number
  offset?: number
  /** Only the geometries intersecting this box are returned when set. */
  bbox?: BBox
}

/** Reads the `{"error": "..."}` body the Go handlers send, falling back to the status text. */
async function failure(response: Response): Promise<Error> {
  const body = await response.text()
  try {
    const parsed = JSON.parse(body) as { error?: string }
    if (parsed.error) return new Error(parsed.error)
  } catch {
    // Not a JSON body; fall through to the raw text.
  }
  return new Error(body.trim() || `${response.status} ${response.statusText}`)
}

export async function fetchStats(signal?: AbortSignal): Promise<TableStats> {
  const response = await fetch('/api/stats', { signal })
  if (!response.ok) throw await failure(response)
  return (await response.json()) as TableStats
}

export async function runSQL(sql: string, signal?: AbortSignal): Promise<QueryResult> {
  const response = await fetch('/api/sql', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ sql }),
    signal,
  })
  if (!response.ok) throw await failure(response)
  return (await response.json()) as QueryResult
}

/**
 * The DuckDB SQL the `/sparql` endpoint would run a SPARQL query as. Nothing is
 * run; the SPARQL tab shows this beside the editor to explain what a query does
 * under the hood. A query the translator does not support rejects with the
 * same message `/sparql` would answer with.
 */
export async function translateSparql(query: string, signal?: AbortSignal): Promise<string> {
  const response = await fetch('/api/sparql/translate', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ query }),
    signal,
  })
  if (!response.ok) throw await failure(response)
  return ((await response.json()) as { sql: string }).sql
}

/**
 * Clones, builds, and runs a SAL module so that it publishes its ontology. The
 * first inspection of a module can take minutes, since its image has to be built.
 */
export async function inspectModule(module: string, signal?: AbortSignal): Promise<ModuleOntology> {
  const response = await fetch(`/api/salmodule?module=${encodeURIComponent(module)}`, { signal })
  if (!response.ok) throw await failure(response)
  return (await response.json()) as ModuleOntology
}

/**
 * Fetches a document the project pinned: a vocabulary or imported ontology by
 * its SHA-256 digest, or a salmodule:// vocabulary by the git commit hash of
 * its module. The server accepts either name with or without its urn: prefix,
 * so the prefix is stripped client side too, both to normalize what is sent
 * and to name the downloaded file by the bare hash.
 */
export async function resolveBlob(hash: string, signal?: AbortSignal): Promise<BlobResult> {
  const digest = hash.trim().replace(/^urn:(sha256|git-commit-hash):/i, '')
  if (!digest) throw new Error('Enter a blob hash')
  const response = await fetch(`/blobs/${encodeURIComponent(digest)}`, { signal })
  if (!response.ok) throw await failure(response)
  const blob = await response.blob()
  const version = `${digest.length === 40 ? 'urn:git-commit-hash:' : 'urn:sha256:'}${digest}`
  return { digest, version, blob, contentType: response.headers.get('Content-Type') }
}

export async function fetchGeometries(query: GeometryQuery, signal?: AbortSignal): Promise<GeoJSONFeatureCollection> {
  const params = new URLSearchParams()
  if (query.limit !== undefined) params.set('limit', String(query.limit))
  if (query.offset !== undefined) params.set('offset', String(query.offset))
  if (query.bbox) params.set('bbox', query.bbox.join(','))
  const response = await fetch(`/geometries?${params}`, {
    headers: { Accept: 'application/geo+json' },
    signal,
  })
  if (!response.ok) throw await failure(response)
  return (await response.json()) as GeoJSONFeatureCollection
}

/** The bounding box of every geometry in the table, as a feature whose geometry is the envelope. */
export async function fetchExtent(signal?: AbortSignal): Promise<GeoJSONFeature> {
  const response = await fetch('/geometries/extent', {
    headers: { Accept: 'application/geo+json' },
    signal,
  })
  if (!response.ok) throw await failure(response)
  return (await response.json()) as GeoJSONFeature
}
