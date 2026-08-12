import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { ControlsContainer, FullScreenControl, SigmaContainer, useRegisterEvents, useSigma, ZoomControl } from '@react-sigma/core'
import '@react-sigma/core/lib/style.css'
import { EdgeArrowProgram } from 'sigma/rendering'
import type { Parser } from '@zazuko/yasr'
import { buildGraph, isGraphFailure, type GraphEdgeAttributes, type GraphNodeAttributes, type NodeKind } from './graphData'
import { drawNodeHover } from './graphTheme'
import { mocha } from '../../theme'

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
 * The measurement itself is synchronous (getBoundingClientRect in a layout effect,
 * which forces a reflow) rather than waiting on ResizeObserver's first async callback —
 * on this component's very first mount that callback landing a tick later than expected
 * was enough to occasionally still hit "container has no width/height". ResizeObserver
 * stays wired up for later changes (window/panel resize).
 */
function useElementSize() {
  const ref = useRef<HTMLDivElement>(null)
  const [size, setSize] = useState({ width: 0, height: 0 })

  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const measure = () => {
      const rect = el.getBoundingClientRect()
      const width = Math.round(rect.width)
      const height = Math.round(rect.height)
      setSize((prev) => (prev.width === width && prev.height === height ? prev : { width, height }))
    }
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(el)
    return () => observer.disconnect()
  }, [])

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

export function GraphView({ vars, bindings }: { vars: string[]; bindings: Parser.Binding[] }) {
  // This component's React root persists across query re-runs (GraphPlugin.draw() only
  // reuses/re-renders it), so a query that toggles between graphable and non-graphable
  // shapes must not change which hooks run — both branches below need this called first.
  const [sizeRef, size] = useElementSize()
  const result = useMemo(() => buildGraph(vars, bindings), [vars, bindings])

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

  return (
    <div className="graph-plugin-body">
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
              labelSize: 12,
              edgeLabelColor: { color: mocha.subtext0 },
              edgeLabelSize: 10,
              defaultNodeColor: mocha.blue,
              defaultEdgeColor: mocha.overlay2,
              defaultDrawNodeHover: drawNodeHover,
              // Sigma skips edge hit-testing by default (it's pricier than node picking);
              // without this, hovering an edge/arrow never fires enterEdge at all.
              enableEdgeEvents: true,
            }}
          >
            <GraphTooltip />
            <ControlsContainer position="bottom-right">
              <ZoomControl />
              <FullScreenControl />
            </ControlsContainer>
          </SigmaContainer>
        )}
      </div>
      <div className="graph-footer">
        <div className="graph-legend">
          {LEGEND.filter((entry) => kindsPresent.has(entry.kind)).map((entry) => (
            <span key={entry.kind} className="graph-legend-item">
              <span className="graph-legend-swatch" style={{ background: mocha[entry.swatch] }} />
              {entry.label}
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
