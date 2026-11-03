/**
 * The form's draft of a SAL module ontology, how an existing ontology.jsonld
 * becomes one, and how a draft becomes the ontology.jsonld text the server
 * checks and writes. Everything here is pure: the server does the checks that
 * need the SAL Module specification, this file only assembles the document and
 * catches what would make assembling it meaningless, such as a shape that is
 * not JSON.
 *
 * A draft keeps whatever it does not have a field for, so that editing a
 * hand-written ontology in the form never drops a statement: context entries
 * that are not prefixes, keys of the ontology node and the task classes the
 * form does not show, graph nodes that are neither, and a shape the structured
 * editor cannot represent, which is edited as JSON instead.
 */

/**
 * The prefixes every module ontology binds, the ones `sal init --salmodule`
 * writes without the form: the SAL module vocabulary itself, OWL and RDFS for
 * the ontology and class nodes, Dublin Core Terms for the title and creator, SHACL
 * for the shapes, and XSD for the datatypes they constrain.
 */
export const BASE_PREFIXES: Record<string, string> = {
  dcterms: 'http://purl.org/dc/terms/',
  owl: 'http://www.w3.org/2002/07/owl#',
  rdfs: 'http://www.w3.org/2000/01/rdf-schema#',
  salmodule: 'https://w3id.org/sal/salmodule#',
  sh: 'http://www.w3.org/ns/shacl#',
  xsd: 'http://www.w3.org/2001/XMLSchema#',
}

/** A task ID is a bare name usable as a relative IRI and as a JSON-LD @type. */
export const ID_PATTERN = /^[A-Za-z][A-Za-z0-9_]*$/
/** A prefix is what goes before the colon in a term such as schema:name. */
export const PREFIX_PATTERN = /^[A-Za-z][A-Za-z0-9_-]*$/
/** A namespace is dereferenceable and ends where a term is joined onto it. */
const NAMESPACE_PATTERN = /^https?:\/\/\S+[/#]$/
/** A term in a shape outside the module's own namespace: prefixed, or an absolute IRI. */
const TERM_PATTERN = /^([A-Za-z][A-Za-z0-9_-]*:[^\s]+)$/
/** Schemes a term written as scheme:rest may use without a prefix binding, since JSON-LD reads it as an absolute IRI. */
const IRI_SCHEMES = new Set(['http', 'https', 'urn', 'file', 'salmodule', 'oci', 'tag', 'did'])

export const SHAPE_KINDS = ['taskShape', 'stdoutShape', 'stdinShape'] as const
export type ShapeKind = (typeof SHAPE_KINDS)[number]

export const SHAPE_LABELS: Record<ShapeKind, string> = {
  taskShape: 'task shape',
  stdoutShape: 'stdout shape',
  stdinShape: 'stdin shape',
}

/**
 * What a property's values are, in the words of the form. Each maps to the
 * SHACL constraint that says it, and the task shape adds the rdfs:range that
 * declares the property with it.
 */
export const VALUE_KINDS = [
  { kind: 'string', label: 'text', datatype: 'xsd:string' },
  { kind: 'integer', label: 'integer', datatype: 'xsd:integer' },
  { kind: 'decimal', label: 'decimal number', datatype: 'xsd:decimal' },
  { kind: 'boolean', label: 'true or false', datatype: 'xsd:boolean' },
  { kind: 'date', label: 'date', datatype: 'xsd:date' },
  { kind: 'dateTime', label: 'date and time', datatype: 'xsd:dateTime' },
  { kind: 'anyURI', label: 'URL (as text)', datatype: 'xsd:anyURI' },
  { kind: 'iri', label: 'IRI (a link to another node)', datatype: '' },
  { kind: 'any', label: 'anything', datatype: '' },
] as const
export type ValueKind = (typeof VALUE_KINDS)[number]['kind']

const DATATYPE_KINDS: Record<string, ValueKind> = Object.fromEntries(
  VALUE_KINDS.filter((entry) => entry.datatype).map((entry) => [entry.datatype, entry.kind]),
)

export type PrefixDraft = { key: number; prefix: string; namespace: string }

/** One sh:property, as the structured editor holds it. */
export type PropertyDraft = {
  key: number
  /** The property's term: a bare name in a task shape, a prefixed term or IRI elsewhere. */
  path: string
  comment: string
  kind: ValueKind
  /** At least one value (sh:minCount 1). */
  required: boolean
  /** At most one value (sh:maxCount 1). */
  single: boolean
  message: string
}

/**
 * A shape as the form holds it. The structured fields are what the editor
 * shows in form mode and `json` is what it shows in JSON mode; the mode says
 * which is current, and switching modes converts one into the other, which is
 * only possible from JSON when the shape holds nothing but what the form
 * knows.
 */
export type ShapeDraft = {
  declared: boolean
  mode: 'form' | 'json'
  /** The sh:targetClass; fixed to the task ID for a task shape. */
  targetClass: string
  properties: PropertyDraft[]
  json: string
}

export type TaskDraft = {
  key: number
  id: string
  label: string
  comment: string
  shapes: Record<ShapeKind, ShapeDraft>
  /** Keys of the class node the form has no field for, written back as they were. */
  extra: Record<string, unknown>
}

export type ModuleDraft = {
  title: string
  creator: string
  version: string
  comment: string
  prefixes: PrefixDraft[]
  tasks: TaskDraft[]
  /** @context entries that are not prefix bindings, such as term definitions, kept as they were. */
  contextExtra: Record<string, unknown>
  /** Keys of the ontology node the form has no field for, kept as they were. */
  ontologyExtra: Record<string, unknown>
  /** Graph nodes that are neither the ontology node nor a task class, kept as they were. */
  otherNodes: unknown[]
}

let nextKey = 1

export function newPrefix(): PrefixDraft {
  return { key: nextKey++, prefix: '', namespace: '' }
}

export function newProperty(kind: ShapeKind): PropertyDraft {
  return { key: nextKey++, path: '', comment: '', kind: kind === 'taskShape' ? 'string' : 'any', required: true, single: true, message: '' }
}

export function newShape(kind: ShapeKind): ShapeDraft {
  return { declared: kind === 'taskShape', mode: 'form', targetClass: '', properties: [], json: '' }
}

export function newTask(): TaskDraft {
  return {
    key: nextKey++,
    id: '',
    label: '',
    comment: '',
    shapes: { taskShape: newShape('taskShape'), stdoutShape: newShape('stdoutShape'), stdinShape: newShape('stdinShape') },
    extra: {},
  }
}

/** The version a new module starts at. */
export const DEFAULT_VERSION = '0.1'

export function newDraft(title: string, creator: string): ModuleDraft {
  return {
    title,
    creator,
    version: DEFAULT_VERSION,
    comment: '',
    prefixes: [],
    tasks: [newTask()],
    contextExtra: {},
    ontologyExtra: {},
    otherNodes: [],
  }
}

/**
 * A worked example of a property for each shape, added by the editor's
 * button to edit from. The output and input examples use Dublin Core Terms,
 * which every module ontology binds, so the example never names a prefix
 * the document lacks.
 */
export function exampleProperty(kind: ShapeKind): PropertyDraft {
  if (kind === 'taskShape') {
    return {
      ...newProperty(kind),
      path: 'maxRetries',
      comment: 'How many times to retry a failed request before giving up.',
      kind: 'integer',
      message: 'maxRetries must be set to exactly one integer.',
    }
  }
  return { ...newProperty(kind), path: 'dcterms:title', comment: '', kind: 'string', message: 'Every node has exactly one title.' }
}

/** The target class a stdout or stdin example uses; a class of a prefix every module binds. */
export const EXAMPLE_TARGET = 'dcterms:Location'

/**
 * The JSON a shape declared straight as JSON starts from: a node shape with
 * its target and an empty property list, for pasting SHACL into.
 */
export function shapeTemplateText(kind: ShapeKind, taskID: string): string {
  return JSON.stringify({ '@type': 'sh:NodeShape', 'sh:targetClass': { '@id': kind === 'taskShape' ? taskID : '' }, 'sh:property': [] }, null, 2)
}

/** The JSON of the worked example of a shape, for the JSON editor's example button. */
export function exampleShapeText(kind: ShapeKind, taskID: string): string {
  return shapeText(kind, taskID, { ...newShape(kind), declared: true, targetClass: EXAMPLE_TARGET, properties: [exampleProperty(kind)] })
}

/**
 * The structured reading of a shape's JSON text, or null when the text is
 * not JSON or holds what the rows cannot show, so the editor knows whether
 * it may switch to rows.
 */
export function shapeRows(kind: ShapeKind, taskID: string, json: string): ShapeDraft | null {
  let parsed: unknown
  try {
    parsed = JSON.parse(json)
  } catch {
    return null
  }
  const shape = shapeFromJSON(kind, taskID, parsed)
  return shape.mode === 'form' ? shape : null
}

// ---------- draft to JSON ----------

type Node = Record<string, unknown>

/** The JSON-LD of one property constraint, as the init skeleton and the specification write it. */
function propertyJSON(kind: ShapeKind, taskID: string, property: PropertyDraft): Node {
  const path = property.path.trim()
  const datatype = VALUE_KINDS.find((entry) => entry.kind === property.kind)?.datatype ?? ''
  let pathNode: Node = { '@id': path }
  if (kind === 'taskShape') {
    // a configuration property is the module's own, so the shape declares
    // it where it constrains it, which is what lets a project look it up
    pathNode = {
      '@id': path,
      '@type': property.kind === 'iri' ? 'owl:ObjectProperty' : 'owl:DatatypeProperty',
      ...(property.comment.trim() ? { 'rdfs:comment': property.comment.trim() } : {}),
      'rdfs:domain': { '@id': taskID },
      ...(datatype ? { 'rdfs:range': { '@id': datatype } } : {}),
    }
  }
  const node: Node = { 'sh:path': pathNode }
  if (kind !== 'taskShape' && property.comment.trim()) node['sh:description'] = property.comment.trim()
  if (datatype) node['sh:datatype'] = { '@id': datatype }
  if (property.kind === 'iri') node['sh:nodeKind'] = { '@id': 'sh:IRI' }
  if (property.required) node['sh:minCount'] = 1
  if (property.single) node['sh:maxCount'] = 1
  if (property.message.trim()) node['sh:message'] = property.message.trim()
  return node
}

/** The JSON-LD of a shape in form mode. */
export function shapeJSON(kind: ShapeKind, taskID: string, shape: ShapeDraft): Node {
  const target = kind === 'taskShape' ? taskID : shape.targetClass.trim()
  return {
    '@type': 'sh:NodeShape',
    ...(target ? { 'sh:targetClass': { '@id': target } } : {}),
    'sh:property': shape.properties.map((property) => propertyJSON(kind, taskID, property)),
  }
}

/** The JSON text of a shape, for switching the editor to JSON mode. */
export function shapeText(kind: ShapeKind, taskID: string, shape: ShapeDraft): string {
  return JSON.stringify(shapeJSON(kind, taskID, shape), null, 2)
}

// ---------- JSON to draft ----------

function isNode(value: unknown): value is Node {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

/** The IRI a value names: a string, or the @id of an object. */
function idOf(value: unknown): string | null {
  if (typeof value === 'string') return value
  if (isNode(value) && typeof value['@id'] === 'string') return value['@id']
  return null
}

function ids(value: unknown): string[] {
  const list = Array.isArray(value) ? value : value === undefined ? [] : [value]
  return list.map(idOf).filter((id): id is string => id !== null)
}

/**
 * Reads one sh:property into the structured form, or null when it holds a
 * constraint the form has no field for, in which case the whole shape is
 * edited as JSON.
 */
function propertyFromJSON(kind: ShapeKind, taskID: string, value: unknown): PropertyDraft | null {
  if (!isNode(value)) return null
  const property = newProperty(kind)
  const known = new Set(['sh:path', 'sh:datatype', 'sh:nodeKind', 'sh:minCount', 'sh:maxCount', 'sh:message', 'sh:description'])
  for (const key of Object.keys(value)) if (!known.has(key)) return null

  const path = value['sh:path']
  const id = idOf(path)
  if (!id) return null
  property.path = id
  if (isNode(path)) {
    // the inline declaration of a configuration property; anything beyond
    // what the form writes there is not representable
    const declared = new Set(['@id', '@type', 'rdfs:comment', 'rdfs:domain', 'rdfs:range'])
    for (const key of Object.keys(path)) if (!declared.has(key)) return null
    if (kind !== 'taskShape' && Object.keys(path).length > 1) return null
    if (kind === 'taskShape') {
      const type = path['@type']
      if (type !== undefined && type !== 'owl:DatatypeProperty' && type !== 'owl:ObjectProperty') return null
      if (path['rdfs:domain'] !== undefined && idOf(path['rdfs:domain']) !== taskID) return null
      if (typeof path['rdfs:comment'] === 'string') property.comment = path['rdfs:comment']
      else if (path['rdfs:comment'] !== undefined) return null
    }
  }

  const datatype = value['sh:datatype'] === undefined ? null : idOf(value['sh:datatype'])
  const nodeKind = value['sh:nodeKind'] === undefined ? null : idOf(value['sh:nodeKind'])
  if (datatype) {
    const found = DATATYPE_KINDS[datatype]
    if (!found || nodeKind) return null
    property.kind = found
  } else if (nodeKind === 'sh:IRI') {
    property.kind = 'iri'
  } else if (nodeKind) {
    return null
  } else {
    property.kind = 'any'
  }
  if (isNode(path) && path['rdfs:range'] !== undefined) {
    const range = idOf(path['rdfs:range'])
    const expected = VALUE_KINDS.find((entry) => entry.kind === property.kind)?.datatype
    if (range !== expected) return null
  }

  const min = value['sh:minCount']
  if (min === undefined || min === 0) property.required = false
  else if (min === 1) property.required = true
  else return null
  const max = value['sh:maxCount']
  if (max === undefined) property.single = false
  else if (max === 1) property.single = true
  else return null

  if (typeof value['sh:message'] === 'string') property.message = value['sh:message']
  else if (value['sh:message'] !== undefined) return null
  if (typeof value['sh:description'] === 'string') property.comment = value['sh:description']
  else if (value['sh:description'] !== undefined) return null
  return property
}

/**
 * Reads a shape annotation into a draft: into the structured form when it is
 * one node shape holding only what the form shows, and as JSON otherwise so
 * that nothing is lost.
 */
export function shapeFromJSON(kind: ShapeKind, taskID: string, value: unknown): ShapeDraft {
  const shape = newShape(kind)
  if (value === undefined) return { ...shape, declared: false }
  shape.declared = true
  shape.mode = 'json'
  shape.json = JSON.stringify(value, null, 2)
  if (!isNode(value)) return shape
  const known = new Set(['@type', 'sh:targetClass', 'sh:property'])
  for (const key of Object.keys(value)) if (!known.has(key)) return shape
  if (value['@type'] !== undefined && value['@type'] !== 'sh:NodeShape') return shape
  const target = value['sh:targetClass'] === undefined ? '' : idOf(value['sh:targetClass'])
  if (target === null) return shape
  if (kind === 'taskShape' && target && target !== taskID) return shape
  const constraints = value['sh:property'] === undefined ? [] : Array.isArray(value['sh:property']) ? value['sh:property'] : [value['sh:property']]
  const properties: PropertyDraft[] = []
  for (const constraint of constraints) {
    const property = propertyFromJSON(kind, taskID, constraint)
    if (!property) return shape
    properties.push(property)
  }
  return { ...shape, mode: 'form', targetClass: kind === 'taskShape' ? '' : target, properties }
}

/**
 * Reads an ontology.jsonld into a draft, or null when the text is not a
 * JSON-LD document with a @context and a @graph the form can show, which is
 * then edited elsewhere. Every task class, the ontology node, and the
 * prefixes become fields; everything else is kept to be written back.
 */
export function parseDocument(text: string): ModuleDraft | null {
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    return null
  }
  if (!isNode(parsed) || !isNode(parsed['@context']) || !Array.isArray(parsed['@graph'])) return null
  const draft = newDraft('', '')
  draft.tasks = []

  for (const [key, value] of Object.entries(parsed['@context'])) {
    if (typeof value === 'string' && !(key in BASE_PREFIXES)) draft.prefixes.push({ ...newPrefix(), prefix: key, namespace: value })
    else if (typeof value !== 'string') draft.contextExtra[key] = value
  }

  for (const node of parsed['@graph']) {
    if (!isNode(node)) {
      draft.otherNodes.push(node)
      continue
    }
    const types = ids(node['@type'])
    if (types.includes('owl:Ontology')) {
      const { '@id': id, '@type': type, 'dcterms:title': title, 'dcterms:creator': creator, 'owl:versionInfo': version, 'rdfs:comment': comment, ...extra } = node
      if (id !== '.' || type !== 'owl:Ontology') {
        // not the node sal writes; keep it whole rather than reshape it
        draft.otherNodes.push(node)
        continue
      }
      draft.title = typeof title === 'string' ? title : ''
      draft.creator = typeof creator === 'string' ? creator : ''
      draft.version = typeof version === 'string' ? version : ''
      draft.comment = typeof comment === 'string' ? comment : ''
      for (const [key, value] of [['dcterms:title', title], ['dcterms:creator', creator], ['owl:versionInfo', version], ['rdfs:comment', comment]] as const) {
        if (value !== undefined && typeof value !== 'string') extra[key] = value
      }
      draft.ontologyExtra = extra
      continue
    }
    const parents = ids(node['rdfs:subClassOf'])
    const isTask = types.includes('owl:Class') && parents.some((parent) => ['salmodule:Task', 'salmodule:NodeProducer', 'salmodule:NodeConsumer', 'salmodule:NodeProcessor'].includes(parent))
    if (!isTask || typeof node['@id'] !== 'string' || type(node['@type']) !== 'owl:Class' || parents.length !== 1) {
      draft.otherNodes.push(node)
      continue
    }
    const { '@id': id, '@type': _type, 'rdfs:subClassOf': _parent, 'rdfs:label': label, 'rdfs:comment': comment, 'salmodule:taskShape': taskShape, 'salmodule:stdoutShape': stdoutShape, 'salmodule:stdinShape': stdinShape, ...extra } = node
    void _type
    void _parent
    const task = newTask()
    task.id = id
    task.label = typeof label === 'string' ? label : ''
    task.comment = typeof comment === 'string' ? comment : ''
    if (label !== undefined && typeof label !== 'string') extra['rdfs:label'] = label
    if (comment !== undefined && typeof comment !== 'string') extra['rdfs:comment'] = comment
    task.shapes = {
      taskShape: shapeFromJSON('taskShape', id, taskShape),
      stdoutShape: shapeFromJSON('stdoutShape', id, stdoutShape),
      stdinShape: shapeFromJSON('stdinShape', id, stdinShape),
    }
    task.extra = extra
    draft.tasks.push(task)
  }
  return draft
}

/** The one @type of a node, or null when it has several. */
function type(value: unknown): string | null {
  const all = ids(value)
  return all.length === 1 ? all[0] : null
}

// ---------- draft to document ----------

/**
 * The prefix of a term written as prefix:name that the context does not
 * bind and that is not an IRI scheme, or null. JSON-LD would read such a
 * term as an absolute IRI in a scheme named after the typo, which sal's
 * checks cannot tell from one that was meant.
 */
function unboundPrefix(term: string, context: Record<string, unknown>): string | null {
  const colon = term.indexOf(':')
  if (colon <= 0) return null
  const prefix = term.slice(0, colon)
  if (prefix in context || IRI_SCHEMES.has(prefix.toLowerCase()) || term.slice(colon + 1).startsWith('//')) return null
  return prefix
}

export type Assembled = {
  /** The ontology.jsonld text, exactly as it would be written. */
  text: string
  /** What stops the draft from being a document worth checking, in the form's own words. */
  problems: string[]
}

/**
 * Assembles the ontology.jsonld document from the draft: the @context of the
 * base prefixes plus the form's, an owl:Ontology node named "." like the one
 * `sal init --salmodule` writes, and one owl:Class per task that subclasses
 * salmodule:Task and carries the shapes it declares, followed by whatever
 * other nodes the document held. Blank strings are written as blanks rather
 * than left out, so that the server's schema check reports them the way it
 * reports them in an editor.
 */
export function assemble(draft: ModuleDraft): Assembled {
  const problems: string[] = []
  const context: Record<string, unknown> = { ...BASE_PREFIXES }
  draft.prefixes.forEach((entry, index) => {
    const prefix = entry.prefix.trim()
    const namespace = entry.namespace.trim()
    if (!prefix && !namespace) return
    const where = `Prefix ${index + 1}`
    let usable = true
    if (!PREFIX_PATTERN.test(prefix)) {
      problems.push(`${where}: a prefix starts with a letter and holds only letters, digits, _ and -`)
      usable = false
    } else if (prefix in context) {
      problems.push(`${where}: ${prefix} is already bound`)
      usable = false
    }
    if (!NAMESPACE_PATTERN.test(namespace)) {
      problems.push(`${where}: the namespace must be an http(s) IRI ending in / or #`)
      usable = false
    }
    if (usable) context[prefix] = namespace
  })
  Object.assign(context, draft.contextExtra)

  const ontology: Node = {
    '@id': '.',
    '@type': 'owl:Ontology',
    'dcterms:creator': draft.creator.trim(),
    'dcterms:title': draft.title.trim(),
    'owl:versionInfo': draft.version.trim(),
    'rdfs:comment': draft.comment.trim(),
  }
  if (!draft.title.trim()) problems.push('The module needs a title')
  if (!draft.version.trim()) problems.push(`The module needs a version, such as ${DEFAULT_VERSION}`)
  if (!draft.comment.trim()) problems.push('The module needs a description of what it is for')
  // what the node holds beyond these fields, salmodule:taskInstanceEnvVar
  // among them, is not the form's to change
  Object.assign(ontology, draft.ontologyExtra)
  const graph: unknown[] = [ontology]

  if (draft.tasks.length === 0) problems.push('A module declares at least one task')
  const ids = new Set<string>()
  draft.tasks.forEach((task, index) => {
    const id = task.id.trim()
    const where = id ? `Task ${id}` : `Task ${index + 1}`
    if (!id) {
      problems.push(`${where}: needs an ID`)
    } else if (!ID_PATTERN.test(id)) {
      problems.push(`${where}: an ID starts with a letter and holds only letters, digits, and underscores`)
    } else if (ids.has(id)) {
      problems.push(`${where}: another task already has this ID`)
    }
    ids.add(id)
    const node: Node = {
      '@id': id,
      '@type': 'owl:Class',
      'rdfs:subClassOf': { '@id': 'salmodule:Task' },
      'rdfs:label': task.label.trim(),
      'rdfs:comment': task.comment.trim(),
    }
    for (const kind of SHAPE_KINDS) {
      const shape = task.shapes[kind]
      if (!shape.declared) continue
      if (shape.mode === 'json') {
        if (!shape.json.trim()) {
          problems.push(`${where}: the ${SHAPE_LABELS[kind]} is declared as JSON but empty; paste a shape, insert the example, or remove it`)
          continue
        }
        try {
          node[`salmodule:${kind}`] = JSON.parse(shape.json)
        } catch (caught) {
          problems.push(`${where}: the ${SHAPE_LABELS[kind]} is not valid JSON (${caught instanceof Error ? caught.message : String(caught)})`)
        }
        continue
      }
      if (kind !== 'taskShape' && shape.targetClass.trim() && !TERM_PATTERN.test(shape.targetClass.trim())) {
        problems.push(`${where}: the ${SHAPE_LABELS[kind]} targets a class written as a prefixed term such as dcterms:Location or a full IRI`)
      } else if (kind !== 'taskShape' && unboundPrefix(shape.targetClass.trim(), context)) {
        problems.push(`${where}: the ${SHAPE_LABELS[kind]} targets ${shape.targetClass.trim()}, but the prefix ${unboundPrefix(shape.targetClass.trim(), context)} is not bound; add it under Prefixes`)
      }
      const seen = new Set<string>()
      shape.properties.forEach((property, propertyIndex) => {
        const path = property.path.trim()
        const label = `${where}, ${SHAPE_LABELS[kind]} property ${propertyIndex + 1}`
        if (!path) problems.push(`${label}: needs a name`)
        else if (kind === 'taskShape' && !ID_PATTERN.test(path)) problems.push(`${label}: a configuration property is a bare name such as maxRetries`)
        else if (kind !== 'taskShape' && !TERM_PATTERN.test(path)) problems.push(`${label}: written as a prefixed term such as dcterms:title or a full IRI`)
        else if (kind !== 'taskShape' && unboundPrefix(path, context)) problems.push(`${label}: the prefix ${unboundPrefix(path, context)} of ${path} is not bound; add it under Prefixes`)
        else if (seen.has(path)) problems.push(`${label}: ${path} is already constrained`)
        seen.add(path)
      })
      node[`salmodule:${kind}`] = shapeJSON(kind, id, shape)
    }
    Object.assign(node, task.extra)
    graph.push(node)
  })
  graph.push(...draft.otherNodes)

  return { text: JSON.stringify({ '@context': context, '@graph': graph }, null, 2) + '\n', problems }
}
