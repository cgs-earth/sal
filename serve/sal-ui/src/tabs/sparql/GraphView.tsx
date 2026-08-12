import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ControlsContainer, FullScreenControl, SigmaContainer, useRegisterEvents, useSigma, ZoomControl } from '@react-sigma/core'
import '@react-sigma/core/lib/style.css'
import { EdgeArrowProgram } from 'sigma/rendering'
import type { Parser } from '@zazuko/yasr'
import { buildGraph, isGraphFailure, type GraphEdgeAttributes, type GraphNodeAttributes, type NodeKind } from './graphData'
import { drawNodeHover } from './graphTheme'
import { mocha } from '../../theme'
import { buildTermQuery, canQueryFromNode } from './graphTermQuery'
import { getCurrentQueryText, runGeneratedQuery } from './sparqlBridge'

const LEGEND: Array<{ kind: NodeKind; label: string; swatch: keyof typeof mocha }> = [
  { kind: 'uri', label: 'IRI', swatch: 'blue' },
  { kind: 'blank', label: 'Blank node', swatch: 'peach' },
  { kind: 'literal', label: 'Literal', swatch: 'green' },
  { kind: 'root', label: 'Queried resource', swatch: 'mauve' },
]

/**
 * Sigma measures its container's pixel box itself and throws if either axis is 0.
 * Percentage CSS ("width: 100%") only resolves if every ancestor up the chain
 * (Yasr's plugin panel, the app's flex/grid shell) has a definite size at that
 * instant, which isn't reliable here — so this measures the wrapper for real and
 * hands Sigma literal pixel numbers instead of trusting the cascade.
 *
 * A callback ref, not a plain ref read inside a mount-only effect: GraphView
 * returns a completely different element tree when a query fails to graph
 * (see the isGraphFailure branch below), so the div this measures unmounts and
 * a fresh one mounts every time a query toggles between graphable and not —
 * an effect keyed on `[]` would only ever see the very first of those and
 * silently stop measuring (and stop observing resizes on) every one after it,
 * which is what was leaving later successful queries stuck on a blank canvas.
 * A callback ref fires on every one of those mount/unmount transitions, so the
 * measurement and the ResizeObserver are always attached to whichever DOM node
 * is current.
 */
function useElementSize() {
  const [size, setSize] = useState({ width: 0, height: 0 })
  const observer = useRef<ResizeObserver | null>(null)

  const measure = useCallback((el: HTMLDivElement) => {
    const rect = el.getBoundingClientRect()
    const width = Math.round(rect.width)
    const height = Math.round(rect.height)
    setSize((prev) => (prev.width === width && prev.height === height ? prev : { width, height }))
  }, [])

  const ref = useCallback(
    (el: HTMLDivElement | null) => {
      observer.current?.disconnect()
      observer.current = null
      if (!el) return
      measure(el)
      observer.current = new ResizeObserver(() => measure(el))
      observer.current.observe(el)
    },
    [measure],
  )

  return [ref, size] as const
}

interface HoverInfo {
  x: number
  y: number
  kindLabel: string
  fullLabel: string
}

function GraphTooltip() {
  const registerEvents = useRegisterEvents()
  const sigma = useSigma()
  const [hovered, setHovered] = useState<HoverInfo | null>(null)

  useEffect(() => {
    const pointerOf = (original: MouseEvent | TouchEvent) => ('clientX' in original ? original : null)

    registerEvents({
      enterNode: (event) => {
        const pointer = pointerOf(event.event.original)
        if (!pointer) return
        const rect = sigma.getContainer().getBoundingClientRect()
        const attrs = sigma.getGraph().getNodeAttributes(event.node) as GraphNodeAttributes
        setHovered({
          x: pointer.clientX - rect.left,
          y: pointer.clientY - rect.top,
          kindLabel: LEGEND.find((entry) => entry.kind === attrs.kind)?.label ?? '',
          fullLabel: attrs.fullLabel,
        })
      },
      leaveNode: () => setHovered(null),
      // Hovering the edge line/arrow shows the full predicate IRI — edges built from a
      // shape with no predicate column (e.g. a plain ?subject ?object pair) carry no
      // fullLabel, so those are left alone rather than popping an empty tooltip.
      enterEdge: (event) => {
        const pointer = pointerOf(event.event.original)
        if (!pointer) return
        const attrs = sigma.getGraph().getEdgeAttributes(event.edge) as GraphEdgeAttributes
        if (!attrs.fullLabel) return
        const rect = sigma.getContainer().getBoundingClientRect()
        setHovered({
          x: pointer.clientX - rect.left,
          y: pointer.clientY - rect.top,
          kindLabel: 'Predicate',
          fullLabel: attrs.fullLabel,
        })
      },
      leaveEdge: () => setHovered(null),
    })
  }, [registerEvents, sigma])

  if (!hovered) return null
  return (
    <div className="graph-tooltip" style={{ left: hovered.x, top: hovered.y }}>
      <span className="graph-tooltip-kind">{hovered.kindLabel}</span>
      {hovered.fullLabel}
    </div>
  )
}

interface NodeMenuState {
  x: number
  y: number
  attrs: GraphNodeAttributes
}

/**
 * Right-click menu for a graph node: copy its exact term, or seed a fresh
 * SPARQL query from it (the IRI as subject, or the literal as object) and
 * run it in the SPARQL tab. A root node is a synthetic stand-in for a query
 * constant with no real term behind it, so it gets no menu at all.
 */
function GraphContextMenu() {
  const registerEvents = useRegisterEvents()
  const sigma = useSigma()
  const [menu, setMenu] = useState<NodeMenuState | null>(null)
  const [copied, setCopied] = useState(false)

  const close = useCallback(() => {
    setMenu(null)
    setCopied(false)
  }, [])

  useEffect(() => {
    registerEvents({
      rightClickNode: (event) => {
        const original = event.event.original
        original.preventDefault()
        const attrs = sigma.getGraph().getNodeAttributes(event.node) as GraphNodeAttributes
        if (attrs.kind === 'root') return
        const rect = sigma.getContainer().getBoundingClientRect()
        const pointer = 'clientX' in original ? original : undefined
        setCopied(false)
        setMenu({
          x: (pointer?.clientX ?? rect.left) - rect.left,
          y: (pointer?.clientY ?? rect.top) - rect.top,
          attrs,
        })
      },
      clickStage: close,
      clickNode: close,
      clickEdge: close,
    })
  }, [registerEvents, sigma, close])

  useEffect(() => {
    if (!menu) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') close()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [menu, close])

  if (!menu) return null

  const copyTerm = async () => {
    await navigator.clipboard.writeText(menu.attrs.fullLabel)
    setCopied(true)
    setTimeout(close, 700)
  }

  const useInQuery = () => {
    runGeneratedQuery(buildTermQuery(menu.attrs))
    close()
  }

  return (
    <div className="graph-context-menu" style={{ left: menu.x, top: menu.y }}>
      <button type="button" className="graph-context-menu-item" onClick={() => void copyTerm()}>
        {copied ? 'Copied' : 'Copy term'}
      </button>
      {canQueryFromNode(menu.attrs) && (
        <button type="button" className="graph-context-menu-item" onClick={useInQuery}>
          Use in new SPARQL query
        </button>
      )}
    </div>
  )
}

const DEFAULT_LABEL_SIZE = 12
const DEFAULT_SPACING = 1
// Sigma's own default (see sigma/settings). The "Node spacing" slider scales this
// inversely: less spacing means a bigger stage padding, which shrinks how much of
// the canvas the graph is fit into.
const BASE_STAGE_PADDING = 30

export function GraphView({ vars, bindings }: { vars: string[]; bindings: Parser.Binding[] }) {
  // This component's React root persists across query re-runs (GraphPlugin.draw() only
  // reuses/re-renders it), so a query that toggles between graphable and non-graphable
  // shapes must not change which hooks run — both branches below need this called first.
  const [sizeRef, size] = useElementSize()
  // Settings live here rather than in module state: they reset whenever the Graph
  // plugin itself is torn down (switching Yasgui tabs, or leaving the SPARQL tab),
  // the same lifetime as the rest of this component's state.
  const [labelSize, setLabelSize] = useState(DEFAULT_LABEL_SIZE)
  const [spacing, setSpacing] = useState(DEFAULT_SPACING)
  const result = useMemo(() => buildGraph(vars, bindings, { queryText: getCurrentQueryText() }), [vars, bindings])

  if (isGraphFailure(result)) {
    return (
      <div className="graph-empty-state">
        <p>Can't draw this result as a graph.</p>
        <p className="graph-empty-reason">{result.reason}</p>
      </div>
    )
  }

  const kindsPresent = new Set(result.graph.mapNodes((_node, attrs) => attrs.kind))
  const ready = size.width > 0 && size.height > 0
  // buildGraph only ever produces one synthetic root node (a query can fix at most one of
  // subject/object — pickColumns needs the other two positions bound to real columns), so
  // there's no ambiguity in showing its actual term here instead of a generic legend label.
  const rootNode = kindsPresent.has('root') ? result.graph.findNode((_node, attrs) => attrs.kind === 'root') : undefined
  const rootLabel = rootNode ? (result.graph.getNodeAttribute(rootNode, 'label') as string) : undefined

  return (
    <div className="graph-plugin-body">
      <div className="graph-settings">
        <label className="graph-settings-control">
          Label size
          <input
            type="range"
            min={8}
            max={20}
            step={1}
            value={labelSize}
            onChange={(event) => setLabelSize(Number(event.target.value))}
          />
          <span className="graph-settings-value">{labelSize}px</span>
        </label>
        <label className="graph-settings-control">
          Node spacing
          <input
            type="range"
            min={0.2}
            max={3}
            step={0.1}
            value={spacing}
            onChange={(event) => setSpacing(Number(event.target.value))}
          />
          <span className="graph-settings-value">×{spacing.toFixed(1)}</span>
        </label>
        {(labelSize !== DEFAULT_LABEL_SIZE || spacing !== DEFAULT_SPACING) && (
          <button
            type="button"
            className="graph-settings-reset"
            onClick={() => {
              setLabelSize(DEFAULT_LABEL_SIZE)
              setSpacing(DEFAULT_SPACING)
            }}
          >
            Reset
          </button>
        )}
      </div>
      <div ref={sizeRef} className="graph-sigma-container">
        {ready && (
          <SigmaContainer
            // Passed as a prop (rather than loaded post-mount via useLoadGraph) so Sigma
            // constructs directly against this graph instance — multi:true and all. Every
            // fresh query gives buildGraph() a new graph object, so this prop identity
            // changes and Sigma fully tears down and rebuilds against it; the alternative
            // (useLoadGraph's clear()+import() into Sigma's own default *non-multi* graph)
            // ignored our multi setting entirely and could throw on two edges between the
            // same node pair.
            graph={result.graph}
            style={{ width: size.width, height: size.height }}
            settings={{
              renderEdgeLabels: true,
              defaultEdgeType: 'arrow',
              edgeProgramClasses: { arrow: EdgeArrowProgram },
              labelColor: { color: mocha.text },
              labelSize,
              edgeLabelColor: { color: mocha.subtext0 },
              edgeLabelSize: Math.round(labelSize * 0.83),
              // Sigma always scales a graph's node coordinates to fill the camera frame
              // (see the comment on layout() in graphData.ts), so stagePadding — applied
              // after that fit, not before — is what actually makes "Node spacing" shrink
              // or spread a graph, for a 2-node graph exactly as much as a 200-node one.
              stagePadding: Math.round(BASE_STAGE_PADDING / spacing),
              defaultNodeColor: mocha.blue,
              defaultEdgeColor: mocha.overlay2,
              defaultDrawNodeHover: drawNodeHover,
              // Sigma skips edge hit-testing by default (it's pricier than node picking);
              // without this, hovering an edge/arrow never fires enterEdge at all.
              enableEdgeEvents: true,
            }}
          >
            <GraphTooltip />
            <GraphContextMenu />
            {/* Full screen is up top with the rest of the page chrome, out of the way of the
                zoom controls, so it's reachable without hunting for it at the bottom of a graph
                that can be taller than the viewport. */}
            <ControlsContainer position="top-right">
              <FullScreenControl />
            </ControlsContainer>
            <ControlsContainer position="bottom-right">
              <ZoomControl />
            </ControlsContainer>
          </SigmaContainer>
        )}
      </div>
      <div className="graph-footer">
        <div className="graph-legend">
          {LEGEND.filter((entry) => kindsPresent.has(entry.kind)).map((entry) => (
            <span key={entry.kind} className="graph-legend-item">
              <span className="graph-legend-swatch" style={{ background: mocha[entry.swatch] }} />
              {entry.kind === 'root' && rootLabel ? rootLabel : entry.label}
            </span>
          ))}
          {result.predicateVar && <span className="graph-legend-item">Edge labels come from ?{result.predicateVar}</span>}
        </div>
        <div className="graph-stats">
          {result.nodeCount} node{result.nodeCount === 1 ? '' : 's'}, {result.edgeCount} edge{result.edgeCount === 1 ? '' : 's'} from{' '}
          {result.rowsUsed} row{result.rowsUsed === 1 ? '' : 's'}
          {result.rowsTruncated > 0 && ` (${result.rowsTruncated} more rows were not drawn)`}
        </div>
      </div>
    </div>
  )
}
