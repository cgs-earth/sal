import Graph from 'graphology'
import type { Parser } from '@zazuko/yasr'

export type NodeKind = 'uri' | 'literal' | 'blank' | 'root'

export type GraphNodeAttributes = {
  [key: string]: unknown
  x: number
  y: number
  size: number
  color: string
  label: string
  fullLabel: string
  kind: NodeKind
}

export type GraphEdgeAttributes = {
  [key: string]: unknown
  label: string
  fullLabel: string
  size: number
  color: string
  type: string
}

export type TripleGraph = Graph<GraphNodeAttributes, GraphEdgeAttributes>

export interface GraphBuildSuccess {
  graph: TripleGraph
  subjectVar?: string
  predicateVar?: string
  objectVar?: string
  nodeCount: number
  edgeCount: number
  rowsUsed: number
  rowsTruncated: number
}

export interface GraphBuildFailure {
  reason: string
}

export function isGraphFailure(result: GraphBuildSuccess | GraphBuildFailure): result is GraphBuildFailure {
  return 'reason' in result
}

// The DuckDB-backed /sparql endpoint has no LIMIT by default, so an unbounded
// query could hand the force-directed layout tens of thousands of rows.
const MAX_ROWS = 2000

const NODE_COLOR: Record<NodeKind, string> = {
  uri: '#89b4fa', // mocha.blue
  blank: '#fab387', // mocha.peach
  literal: '#a6e3a1', // mocha.green
  root: '#cba6f7', // mocha.mauve
}

const NODE_SIZE: Record<NodeKind, number> = {
  uri: 7,
  blank: 7,
  literal: 4.5,
  root: 10,
}

const EDGE_COLOR = '#7f849c' // mocha.overlay1

const SYNTHETIC_SUBJECT_ID = 'root:subject'
const SYNTHETIC_OBJECT_ID = 'root:object'

interface ColumnPick {
  subjectVar?: string
  predicateVar?: string
  objectVar?: string
}

/**
 * Figures out which columns hold the "from" node, the edge label and the "to" node.
 * subjectVar/objectVar can come back unset — see buildGraph, which fills in a single
 * synthetic root node for whichever side has no column (a query like
 * `<http://.../Thing> ?predicate ?object` never selects the subject at all, since it's
 * a constant baked into the query rather than a bound variable).
 */
function pickColumns(vars: string[]): ColumnPick | undefined {
  const lower = vars.map((v) => v.toLowerCase())
  const find = (...names: string[]) => {
    for (const name of names) {
      const idx = lower.indexOf(name)
      if (idx !== -1) return vars[idx]
    }
    return undefined
  }
  const subjectVar = find('subject', 's')
  const predicateVar = find('predicate', 'p')
  const objectVar = find('object', 'o')
  if (subjectVar && objectVar && subjectVar !== objectVar) {
    return { subjectVar, predicateVar, objectVar }
  }
  // A recognizably-named predicate+object (or subject+predicate) pair with the other
  // side missing means that side was a query constant, not a selected column — describe
  // it as a star around one synthetic root rather than misreading the predicate column's
  // values (IRIs like ?predicate always are) as if they were the "from" nodes.
  if (predicateVar && objectVar && !subjectVar) return { predicateVar, objectVar }
  if (subjectVar && predicateVar && !objectVar) return { subjectVar, predicateVar }
  // No recognizable naming at all: fall back to column position, which only makes
  // sense when there's exactly a from/to pair (optionally with an edge label).
  if (vars.length === 3) return { subjectVar: vars[0], predicateVar: vars[1], objectVar: vars[2] }
  if (vars.length === 2) return { subjectVar: vars[0], objectVar: vars[1] }
  return undefined
}

function kindOf(value: Parser.BindingValue): NodeKind {
  if (value.type === 'bnode') return 'blank'
  if (value.type === 'uri') return 'uri'
  return 'literal'
}

function nodeId(value: Parser.BindingValue): string {
  return `${value.type}:${value.value}`
}

function shorten(value: string, kind: NodeKind): string {
  if (kind === 'literal') return value.length > 42 ? `${value.slice(0, 39)}…` : value
  const cleaned = value.replace(/[/#]+$/, '')
  const cut = Math.max(cleaned.lastIndexOf('#'), cleaned.lastIndexOf('/'))
  const tail = cut === -1 ? cleaned : cleaned.slice(cut + 1)
  return tail || value
}

/** Deterministic starting layout: force-directed refinement runs on top of this. */
function seedCircular(graph: TripleGraph) {
  const nodes = graph.nodes()
  const n = nodes.length
  nodes.forEach((node, i) => {
    const angle = (2 * Math.PI * i) / n
    graph.setNodeAttribute(node, 'x', Math.cos(angle))
    graph.setNodeAttribute(node, 'y', Math.sin(angle))
  })
}

/** A compact Fruchterman-Reingold pass so connected nodes cluster instead of sitting on a ring. */
function layout(graph: TripleGraph) {
  seedCircular(graph)
  const nodes = graph.nodes()
  const n = nodes.length
  if (n <= 1) return

  const iterations = n > 500 ? 40 : n > 150 ? 80 : 150
  const k = Math.sqrt(1 / n)
  const pos = new Map(nodes.map((node) => [node, { x: graph.getNodeAttribute(node, 'x'), y: graph.getNodeAttribute(node, 'y') }]))
  const disp = new Map(nodes.map((node) => [node, { x: 0, y: 0 }]))

  for (let iter = 0; iter < iterations; iter++) {
    for (const node of nodes) disp.set(node, { x: 0, y: 0 })

    for (let i = 0; i < n; i++) {
      const v = nodes[i]
      const pv = pos.get(v)!
      const dv = disp.get(v)!
      for (let j = i + 1; j < n; j++) {
        const u = nodes[j]
        const pu = pos.get(u)!
        let dx = pv.x - pu.x
        let dy = pv.y - pu.y
        const dist = Math.sqrt(dx * dx + dy * dy) || 0.01
        const force = (k * k) / dist
        dx = (dx / dist) * force
        dy = (dy / dist) * force
        dv.x += dx
        dv.y += dy
        const du = disp.get(u)!
        du.x -= dx
        du.y -= dy
      }
    }

    graph.forEachEdge((_edge, _attrs, source, target) => {
      const ps = pos.get(source)!
      const pt = pos.get(target)!
      const dx = ps.x - pt.x
      const dy = ps.y - pt.y
      const dist = Math.sqrt(dx * dx + dy * dy) || 0.01
      const force = (dist * dist) / k
      const fx = (dx / dist) * force
      const fy = (dy / dist) * force
      const ds = disp.get(source)!
      ds.x -= fx
      ds.y -= fy
      const dt = disp.get(target)!
      dt.x += fx
      dt.y += fy
    })

    const temperature = (1 - iter / iterations) * 0.1
    for (const node of nodes) {
      const d = disp.get(node)!
      const dist = Math.sqrt(d.x * d.x + d.y * d.y) || 0.01
      const limited = Math.min(dist, temperature)
      const p = pos.get(node)!
      p.x += (d.x / dist) * limited
      p.y += (d.y / dist) * limited
    }
  }

  for (const node of nodes) {
    const p = pos.get(node)!
    graph.setNodeAttribute(node, 'x', p.x)
    graph.setNodeAttribute(node, 'y', p.y)
  }
}

export function buildGraph(vars: string[], bindings: Parser.Binding[]): GraphBuildSuccess | GraphBuildFailure {
  if (bindings.length === 0) {
    return { reason: 'The query returned no rows to plot.' }
  }

  const columns = pickColumns(vars)
  if (!columns) {
    if (vars.length < 2) {
      return { reason: `This result has a single column (${vars[0] ?? '?'}), so there's no relationship between rows to draw.` }
    }
    return {
      reason:
        `Can't tell which of the ${vars.length} columns (${vars.join(', ')}) are nodes and which is the edge. ` +
        'Name them ?subject/?predicate/?object (or ?s/?p/?o), or return exactly two or three columns, to graph this query.',
    }
  }

  const { subjectVar, predicateVar, objectVar } = columns
  const rowsTruncated = Math.max(0, bindings.length - MAX_ROWS)
  const rows = rowsTruncated > 0 ? bindings.slice(0, MAX_ROWS) : bindings

  const graph: TripleGraph = new Graph({ multi: true, type: 'directed' })
  let rowsUsed = 0

  const addRootNode = (id: string, label: string, fullLabel: string) => {
    if (graph.hasNode(id)) return
    graph.addNode(id, { x: 0, y: 0, size: NODE_SIZE.root, color: NODE_COLOR.root, label, fullLabel, kind: 'root' })
  }

  for (const binding of rows) {
    const subject = subjectVar ? binding[subjectVar] : undefined
    const object = objectVar ? binding[objectVar] : undefined
    // A column that's actually part of this shape (per pickColumns) must have a
    // value for the row to be usable; a column that's absent from the shape entirely
    // just means that side is the synthetic root, added once below regardless of row.
    if (subjectVar && !subject) continue
    if (objectVar && !object) continue
    rowsUsed++

    const predicate = predicateVar ? binding[predicateVar] : undefined
    const subjectId = subject ? nodeId(subject) : SYNTHETIC_SUBJECT_ID
    const objectId = object ? nodeId(object) : SYNTHETIC_OBJECT_ID

    if (subject) {
      if (!graph.hasNode(subjectId)) {
        const kind = kindOf(subject)
        graph.addNode(subjectId, {
          x: 0,
          y: 0,
          size: NODE_SIZE[kind],
          color: NODE_COLOR[kind],
          label: shorten(subject.value, kind),
          fullLabel: subject.value,
          kind,
        })
      }
    } else {
      addRootNode(SYNTHETIC_SUBJECT_ID, '(queried resource)', 'The subject was fixed in the query text, not a selected column, so it has no value to show.')
    }

    if (object) {
      if (!graph.hasNode(objectId)) {
        const kind = kindOf(object)
        graph.addNode(objectId, {
          x: 0,
          y: 0,
          size: NODE_SIZE[kind],
          color: NODE_COLOR[kind],
          label: shorten(object.value, kind),
          fullLabel: object.value,
          kind,
        })
      }
    } else {
      addRootNode(SYNTHETIC_OBJECT_ID, '(queried resource)', 'The object was fixed in the query text, not a selected column, so it has no value to show.')
    }

    const edgeLabel = predicate ? shorten(predicate.value, 'uri') : ''
    const edgeKey = `${subjectId}|${objectId}|${edgeLabel}`
    if (!graph.hasEdge(edgeKey)) {
      graph.addEdgeWithKey(edgeKey, subjectId, objectId, {
        label: edgeLabel,
        fullLabel: predicate?.value ?? '',
        size: 1.5,
        color: EDGE_COLOR,
        type: 'arrow',
      })
    }
  }

  if (graph.order === 0) {
    return { reason: 'None of the rows had both a subject and an object value to connect.' }
  }

  layout(graph)

  return {
    graph,
    subjectVar,
    predicateVar,
    objectVar,
    nodeCount: graph.order,
    edgeCount: graph.size,
    rowsUsed,
    rowsTruncated,
  }
}
