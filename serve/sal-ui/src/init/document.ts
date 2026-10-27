/**
 * The form's draft of a SAL module ontology, and how it becomes the
 * ontology.jsonld text the server checks and writes. Everything here is pure:
 * the server does the checks that need the SAL Module specification, this
 * file only assembles the document and catches what would make assembling it
 * meaningless, such as a shape that is not JSON.
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

export const SHAPE_KINDS = ['taskShape', 'stdoutShape', 'stdinShape'] as const
export type ShapeKind = (typeof SHAPE_KINDS)[number]

export const SHAPE_LABELS: Record<ShapeKind, string> = {
  taskShape: 'task shape',
  stdoutShape: 'stdout shape',
  stdinShape: 'stdin shape',
}

export type PrefixDraft = { key: number; prefix: string; namespace: string }

export type TaskDraft = {
  key: number
  id: string
  label: string
  comment: string
  /** The JSON text of each shape, empty for a shape the task does not declare. */
  shapes: Record<ShapeKind, string>
}

export type ModuleDraft = {
  title: string
  creator: string
  version: string
  comment: string
  prefixes: PrefixDraft[]
  tasks: TaskDraft[]
}

let nextKey = 1

export function newPrefix(): PrefixDraft {
  return { key: nextKey++, prefix: '', namespace: '' }
}

export function newTask(): TaskDraft {
  return {
    key: nextKey++,
    id: '',
    label: '',
    comment: '',
    shapes: { taskShape: taskShapeTemplate(''), stdoutShape: '', stdinShape: '' },
  }
}

export function newDraft(title: string, creator: string): ModuleDraft {
  return { title, creator, version: '', comment: '', prefixes: [], tasks: [newTask()] }
}

/**
 * The task shape a new task starts with, the same one `sal init --salmodule`
 * writes: a node shape targeting the task class with no properties yet. The
 * form keeps the target in step with the ID for as long as the text is still
 * this template, so an edited shape is never rewritten.
 */
export function taskShapeTemplate(id: string): string {
  return JSON.stringify({ '@type': 'sh:NodeShape', 'sh:targetClass': { '@id': id }, 'sh:property': [] }, null, 2)
}

/** A worked example of each shape, inserted by the editor's button to edit from. */
export function shapeExample(kind: ShapeKind, id: string): string {
  switch (kind) {
    case 'taskShape':
      return JSON.stringify(
        {
          '@type': 'sh:NodeShape',
          'sh:targetClass': { '@id': id },
          'sh:property': [
            {
              'sh:path': {
                '@id': 'maxRetries',
                '@type': 'owl:DatatypeProperty',
                'rdfs:comment': 'How many times to retry a failed request before giving up.',
                'rdfs:domain': { '@id': id },
                'rdfs:range': { '@id': 'xsd:integer' },
              },
              'sh:datatype': { '@id': 'xsd:integer' },
              'sh:minInclusive': 0,
              'sh:minCount': 1,
              'sh:maxCount': 1,
              'sh:message': 'maxRetries must be set to exactly one non-negative integer.',
            },
          ],
        },
        null,
        2,
      )
    case 'stdoutShape':
    case 'stdinShape':
      return JSON.stringify(
        {
          '@type': 'sh:NodeShape',
          'sh:targetClass': { '@id': 'https://schema.org/Place' },
          'sh:property': [
            {
              'sh:path': { '@id': 'https://schema.org/name' },
              'sh:minCount': 1,
              'sh:maxCount': 1,
              'sh:message': 'Every place has exactly one name.',
            },
          ],
        },
        null,
        2,
      )
  }
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
 * salmodule:Task and carries the shapes whose editors are not empty. Blank
 * strings are written as blanks rather than left out, so that the server's
 * schema check reports them the way it reports them in an editor.
 */
export function assemble(draft: ModuleDraft): Assembled {
  const problems: string[] = []
  const context: Record<string, string> = { ...BASE_PREFIXES }
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

  const ontology: Record<string, unknown> = {
    '@id': '.',
    '@type': 'owl:Ontology',
    'dcterms:creator': draft.creator.trim(),
    'dcterms:title': draft.title.trim(),
    'owl:versionInfo': draft.version.trim(),
  }
  if (draft.comment.trim()) ontology['rdfs:comment'] = draft.comment.trim()
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
    const node: Record<string, unknown> = {
      '@id': id,
      '@type': 'owl:Class',
      'rdfs:subClassOf': { '@id': 'salmodule:Task' },
      'rdfs:label': task.label.trim(),
      'rdfs:comment': task.comment.trim(),
    }
    for (const kind of SHAPE_KINDS) {
      const text = task.shapes[kind]
      if (!text.trim()) continue
      try {
        node[`salmodule:${kind}`] = JSON.parse(text)
      } catch (caught) {
        problems.push(`${where}: the ${SHAPE_LABELS[kind]} is not valid JSON (${caught instanceof Error ? caught.message : String(caught)})`)
      }
    }
    graph.push(node)
  })

  return { text: JSON.stringify({ '@context': context, '@graph': graph }, null, 2) + '\n', problems }
}
