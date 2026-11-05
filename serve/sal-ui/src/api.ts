import type { Geometry } from 'geojson'

export type QueryResult = {
  sql: string
  header: string[] | null
  rows: string[][] | null
  message: string
}

export type TableStats = {
  tablePath: string
  /** The dcterms:title and dcterms:description sal init records on the project node of .sal/config.jsonld; empty for a remote table or a project that recorded neither. */
  title: string
  description: string
  triples: number
  subjects: number
  predicates: number
  objects: number
  snapshots: QueryResult
  properties: QueryResult
  /** How the table is laid out at its current snapshot, or null with layoutError saying why it could not be read. */
  layout: TableLayout | null
  layoutError?: string
  /** The same listing `sal get vocabularies` prints: header ["vocabulary", "version", "format", "imported"]. */
  vocabularies: QueryResult
  /** The imported data products, each queryable as a view of its own. */
  importedTables: ImportedTable[] | null
  /** Sample queries only the server can write, such as time travel and the import listing. */
  sampleQueries: NamedQuery[] | null
}

/** The Iceberg layout of the triples table: partitioning, files, and how well each column's bounds prune. */
export type TableLayout = {
  partitioning: PartitionField[] | null
  /** Each column of the sort order as "name asc" or "name desc". */
  sortOrder: string[] | null
  /** How many distinct partition values the data files fall in. */
  partitions: number
  dataFiles: FileSummary
  deleteFiles: FileSummary
  outliers: FileOutlier[] | null
  columns: ColumnPruning[] | null
  files: DataFile[] | null
}

export type PartitionField = {
  name: string
  column: string
  transform: string
}

export type FileSummary = {
  count: number
  rows: number
  bytes: number
  averageRows: number
  medianRows: number
  minRows: number
  maxRows: number
}

export type DataFile = {
  /** The file's path under the table root. */
  path: string
  partition: string
  kind: 'data' | 'equality-deletes' | 'position-deletes'
  rows: number
  bytes: number
  sequenceNumber: number
}

/** A data file that stands out: small is under a tenth of the median, large over four times the average, and ratio its rows over that reference. */
export type FileOutlier = {
  file: DataFile
  kind: 'small' | 'large'
  ratio: number
}

/**
 * How a filter on one column fares against the bounds Iceberg records per data
 * file: filesWithBounds data files record a range for it, and a single value can
 * fall in at most maxFilesPerValue of them, which is the most files an equality
 * filter reads. For a geometry it is the files whose bounding box a query can meet
 * plus the files that record neither a box nor a null count proving they hold none.
 */
export type ColumnPruning = {
  name: string
  type: string
  filesWithBounds: number
  maxFilesPerValue: number
  nulls: number
  lower: string
  upper: string
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

export type BlobResult = {
  /** The name the blob was resolved by, with any urn:sha256: or urn:git-commit-hash: prefix stripped. */
  digest: string
  /** The owl:versionIRI form of digest: urn:git-commit-hash: for a 40 character git commit, urn:sha256: otherwise. */
  version: string
  blob: Blob
  contentType: string | null
}

/** One blob the table refers to, as `/api/blobs` lists it. */
export type PinnedBlob = {
  /** What the blob was known by before it was pinned: a vocabulary's namespace, or the file:/// IRI a SAL module task named a copied file or directory with. */
  iri: string
  /** The owl:versionIRI naming the blob, urn:sha256:… or urn:git-commit-hash:…, which /blobs/{hash} resolves. */
  hash: string
  /** The subject's dcterms:modified as text, when it has one: when a vocabulary was pinned, or the mtime of a copied file. */
  modified?: string
}

export type BlobListing = {
  blobs: PinnedBlob[]
  /** Whether the table refers to more blobs than were listed. */
  truncated: boolean
  /** The DuckDB statement that lists the same blobs, for running by hand in the SQL tab. */
  sql: string
}

/** One statement a pinned vocabulary document makes about a term, with the predicate and an IRI value written as prefixed names where a prefix is known. */
export type TermStatement = {
  predicate: string
  value: string
  /** Whether value names a resource rather than being a literal. */
  iri?: boolean
  /** The language tag of a literal value, when it has one. */
  language?: string
}

/** One vocabulary the project pins, with the terms its pinned document defines, as `/api/vocabularies` lists it. */
export type Vocabulary = {
  /** The IRI the pin was recorded under, which is how the Stats tab lists the vocabulary. */
  id: string
  /** The name to declare the namespace under, from the document or by convention. */
  prefix: string
  namespace: string
  /** The owl:versionIRI the vocabulary is pinned at, urn:sha256:… or urn:git-commit-hash:…. */
  version: string
  format: string
  /** Whether the project ontology imports the vocabulary with owl:imports, so its statements are in the table. */
  imported: boolean
  /** The local names under the namespace, each sorted: what the document types as a class, as a property, and the rest. */
  classes: string[]
  properties: string[]
  others: string[]
  /** What the pinned document says about each term, by local name, for every term it describes: its label, comment, definition, superclasses, domain, range, and the like, in that order. */
  statements: Record<string, TermStatement[]>
  /** Why the pinned document could not be read, in which case the term lists are empty. */
  error?: string
  /** A vocabulary the query engine defines rather than the project pins (geof:, uom:, xsd:), with no version. */
  builtin?: boolean
}

export type VocabularyListing = {
  vocabularies: Vocabulary[]
}

/**
 * Lists every vocabulary the project pins with the terms its pinned document
 * defines, read from the blob store the versions a build validated against
 * are kept in. A remote table has no project on disk, so it lists none.
 */
export async function fetchVocabularies(signal?: AbortSignal): Promise<VocabularyListing> {
  const response = await fetch('/api/vocabularies', { signal })
  if (!response.ok) throw await failure(response)
  return (await response.json()) as VocabularyListing
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

/**
 * Reads the `{"error": "..."}` body the Go handlers send, or the
 * `{"code", "description"}` one the OGC API routes send, falling back to
 * the status text.
 */
export async function failure(response: Response): Promise<Error> {
  const body = await response.text()
  try {
    const parsed = JSON.parse(body) as { error?: string; description?: string }
    if (parsed.error) return new Error(parsed.error)
    if (parsed.description) return new Error(parsed.description)
  } catch {
    // Not a JSON body; fall through to the raw text.
  }
  return new Error(body.trim() || `${response.status} ${response.statusText}`)
}

/** The table the server's queries read: a table on object storage, or the project's own when empty. */
export type TableLocation = {
  /** The gs://, s3://, r2://, az://, or https:// root of the remote table, or '' for the local table. */
  remoteTablePath: string
}

/** The remote table schemes the server accepts, which is what the header's input says it takes. */
export const REMOTE_TABLE_SCHEMES = ['s3://', 'gs://', 'r2://', 'az://', 'https://']

export async function fetchTable(signal?: AbortSignal): Promise<TableLocation> {
  const response = await fetch('/api/table', { signal })
  if (!response.ok) throw await failure(response)
  return (await response.json()) as TableLocation
}

/**
 * Points every query the server answers, SPARQL, SQL, stats, and geometries
 * alike, at the triples table at remoteTablePath, or back at the project's
 * own table when it is empty. The server opens the table before switching, so
 * a location it cannot read as a triples table rejects with its error and
 * leaves the current table in place. Only object storage is accepted; a path
 * on the server's own machine is refused.
 */
export async function switchTable(remoteTablePath: string, signal?: AbortSignal): Promise<TableLocation> {
  const response = await fetch('/api/table', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ remoteTablePath }),
    signal,
  })
  if (!response.ok) throw await failure(response)
  return (await response.json()) as TableLocation
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

/** One operator of a DuckDB query plan with the operators it reads from. */
export interface PlanNode {
  name: string
  /** What DuckDB says about the operator, in the order it wrote it; a value written on several lines arrives as several values. */
  details: { key: string; values: string[] }[]
  children: PlanNode[]
}

/**
 * How DuckDB would answer a statement, at the three stages its EXPLAIN
 * reports: the logical plan bound from the statement, that plan after the
 * optimizer, and the physical operators that would run. Each is the roots of
 * a tree, one per statement.
 */
export interface QueryPlan {
  logical: PlanNode[]
  optimized: PlanNode[]
  physical: PlanNode[]
}

/**
 * The plans DuckDB would run a statement as. The statement is bound and
 * optimized, which reads the table's metadata, but never run; the SPARQL tab
 * shows them beside the SQL its query translates to.
 */
export async function explainSql(sql: string, signal?: AbortSignal): Promise<QueryPlan> {
  const response = await fetch('/api/sql/explain', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ sql }),
    signal,
  })
  if (!response.ok) throw await failure(response)
  return (await response.json()) as QueryPlan
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

/**
 * Lists the first `limit` blobs the table refers to: every vocabulary a build
 * pinned and every file or directory a SAL module task handed over, each by
 * the hash `/blobs/{hash}` serves it at. The listing is read from the triples
 * table itself, so a blob is listed once a build has recorded it there.
 */
export async function fetchBlobs(limit: number, signal?: AbortSignal): Promise<BlobListing> {
  const response = await fetch(`/api/blobs?limit=${limit}`, { signal })
  if (!response.ok) throw await failure(response)
  return (await response.json()) as BlobListing
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

/** Where `sal serve` answers with the STAC Collection `sal build` writes for the triples table. */
export const STAC_COLLECTION_PATH = '/stac/triples/collection.json'

/** The root of the STAC catalog sal build wrote: the triples table and every catalog a SAL module produced. */
export const STAC_CATALOG_PATH = '/stac/'

/**
 * The catalog opened in the Portolan STAC Browser, which reads any catalog it
 * is handed under #/external/ with the "://" of the URL collapsed to ":/". The
 * root is opened rather than the table's collection so that the catalogs SAL
 * modules handed over are in view beside it.
 */
export function stacViewerURL(origin: string): string {
  return `https://browser.portolan-sdi.org/#/external/${origin.replace('://', ':/')}${STAC_CATALOG_PATH}`
}

/** Whether a STAC catalog has been built for the table, so links to it are offered only when it answers. */
export async function hasStacCatalog(signal?: AbortSignal): Promise<boolean> {
  const response = await fetch(STAC_COLLECTION_PATH, { method: 'HEAD', signal })
  return response.ok
}

/* ---------- OGC API Features ---------- */

/** Where `sal serve` answers OGC API Features: the landing page, with the collection and its items beneath it. */
export const OGC_PATH = '/ogc'

/** The one collection the endpoint serves. */
export const OGC_COLLECTION = 'triples'

export type OgcLink = {
  href: string
  rel: string
  type?: string
  title?: string
}

/** The landing page: the data product's name and the links into the API. */
export type OgcLanding = {
  title: string
  description: string
  links: OgcLink[]
}

export type OgcCollection = {
  id: string
  title: string
  description: string
  itemType: string
  crs: string[]
  /** Absent when the table holds no geometries. */
  extent?: { spatial: { bbox: BBox[]; crs: string } }
  links: OgcLink[]
}

/**
 * One feature: an RDF subject with a geometry, under its IRI, carrying the
 * predicates the request named as properties keyed by their IRI, null where
 * the subject lacks one, an array where it has several values.
 */
export type OgcFeature = {
  type: 'Feature'
  id: string
  geometry: Geometry | null
  properties: Record<string, unknown>
  links?: OgcLink[]
}

export type OgcFeatureCollection = {
  type: 'FeatureCollection'
  timeStamp: string
  numberReturned: number
  features: OgcFeature[]
  links: OgcLink[]
}

/** The parameters the items and item routes take; everything optional reads the current table. */
export type OgcQuery = {
  /** The Iceberg snapshot to read the table at, '' for the current one. */
  snapshot?: string
  /** The predicate IRIs each feature carries as properties. */
  properties?: string[]
  /** Only the features typed with this class IRI (items only). */
  type?: string
  limit?: number
  offset?: number
  bbox?: BBox
}

/** The relative URL of a page of features. */
export function ogcItemsURL(query: OgcQuery): string {
  const params = ogcParams(query)
  if (query.type) params.set('type', query.type)
  if (query.limit !== undefined) params.set('limit', String(query.limit))
  if (query.offset) params.set('offset', String(query.offset))
  if (query.bbox) params.set('bbox', query.bbox.join(','))
  return withParams(`${OGC_PATH}/collections/${OGC_COLLECTION}/items`, params)
}

/** The relative URL of one feature, its IRI escaped into a single path segment. */
export function ogcItemURL(id: string, query: OgcQuery): string {
  return withParams(`${OGC_PATH}/collections/${OGC_COLLECTION}/items/${encodeURIComponent(id)}`, ogcParams(query))
}

function ogcParams(query: OgcQuery): URLSearchParams {
  const params = new URLSearchParams()
  if (query.snapshot) params.set('snapshot', query.snapshot)
  if (query.properties && query.properties.length > 0) params.set('properties', query.properties.join(','))
  return params
}

function withParams(path: string, params: URLSearchParams): string {
  const encoded = params.toString()
  return encoded ? `${path}?${encoded}` : path
}

async function fetchOgc<T>(path: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(path, { headers: { Accept: 'application/geo+json, application/json' }, signal })
  if (!response.ok) throw await failure(response)
  return (await response.json()) as T
}

export function fetchOgcLanding(signal?: AbortSignal): Promise<OgcLanding> {
  return fetchOgc<OgcLanding>(OGC_PATH, signal)
}

/** The conformance classes the endpoint declares. */
export async function fetchOgcConformance(signal?: AbortSignal): Promise<string[]> {
  const declaration = await fetchOgc<{ conformsTo: string[] }>(`${OGC_PATH}/conformance`, signal)
  return declaration.conformsTo
}

/** The collection, with its extent at the snapshot or at the current table. */
export function fetchOgcCollection(snapshot: string, signal?: AbortSignal): Promise<OgcCollection> {
  const params = new URLSearchParams()
  if (snapshot) params.set('snapshot', snapshot)
  return fetchOgc<OgcCollection>(withParams(`${OGC_PATH}/collections/${OGC_COLLECTION}`, params), signal)
}

export function fetchOgcItems(query: OgcQuery, signal?: AbortSignal): Promise<OgcFeatureCollection> {
  return fetchOgc<OgcFeatureCollection>(ogcItemsURL(query), signal)
}

/** One feature by its subject IRI, or null when the table has no such feature at that snapshot. */
export async function fetchOgcItem(id: string, query: OgcQuery, signal?: AbortSignal): Promise<OgcFeature | null> {
  const response = await fetch(ogcItemURL(id, query), { headers: { Accept: 'application/geo+json' }, signal })
  if (response.status === 404) return null
  if (!response.ok) throw await failure(response)
  return (await response.json()) as OgcFeature
}
