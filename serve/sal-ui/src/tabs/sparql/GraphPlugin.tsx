import { createRoot, type Root } from 'react-dom/client'
import { Suspense } from 'react'
import type Yasr from '@zazuko/yasr'
import type { Plugin } from '@zazuko/yasr'
import type Yasgui from '@zazuko/yasgui'
import { LazyGraphView as GraphView } from './LazyGraphView'
import { GraphLoadingFallback } from './GraphLoadingFallback'

const ICON_SVG =
  '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">' +
  '<circle cx="5" cy="6" r="2.4"/><circle cx="19" cy="6" r="2.4"/><circle cx="12" cy="18" r="2.4"/>' +
  '<path d="M7.2 7.3l7.6 8.7M16.8 7.3l-7.6 8.7M7.4 6h9.2"/></svg>'

function svgElement(markup: string): Element {
  const wrapper = document.createElement('span')
  wrapper.innerHTML = markup
  return wrapper.firstElementChild as Element
}

export interface GraphPluginConfig {}

export default class GraphPlugin implements Plugin<GraphPluginConfig> {
  public priority = 5
  public label = 'Graph'
  private yasr: Yasr
  private container: HTMLDivElement | undefined
  private root: Root | undefined

  constructor(yasr: Yasr) {
    this.yasr = yasr
    // Yasr only recomputes each tab button's disabled state from canHandleResults()
    // once the FIRST query has actually returned — its own draw() bails out before that
    // point, leaving every button in its default (enabled) look. queueMicrotask runs this
    // right after Yasr's own constructor finishes building the button (still earlier than
    // any paint), so the Graph tab starts out visibly disabled instead of defaulting to
    // clickable-but-empty. The 'drawn' listener keeps it in sync afterward as a backstop
    // alongside Yasr's own native updates.
    queueMicrotask(() => this.syncSelectorState())
    this.yasr.on('drawn', () => this.syncSelectorState())
  }

  canHandleResults() {
    return !!this.yasr.results && this.yasr.results.getVariables().length > 0
  }

  getIcon() {
    return svgElement(ICON_SVG)
  }

  private syncSelectorState() {
    const button = this.yasr.rootEl.querySelector('.select_graph')
    button?.classList.toggle('disabled', !this.canHandleResults())
  }

  draw() {
    if (!this.container) {
      this.container = document.createElement('div')
      this.container.className = 'graph-plugin-root'
      this.root = createRoot(this.container)
    }
    // Re-running a query while Graph is already the selected plugin doesn't go through
    // destroy(): Yasr's own draw() wipes resultsEl with raw `.remove()` calls first (see
    // its index.ts, the "make sure to clear the object _here_" comment) and calls
    // draw() again on the SAME plugin instance — silently detaching our container without
    // telling us. Re-appending here (independent of whether the container already existed)
    // is what makes the graph reappear instead of rendering into an orphaned node.
    if (this.container.parentNode !== this.yasr.resultsEl) {
      this.yasr.resultsEl.appendChild(this.container)
    }
    const vars = this.yasr.results?.getVariables() ?? []
    const bindings = this.yasr.results?.getBindings() ?? []
    this.root?.render(
      <Suspense fallback={<GraphLoadingFallback />}>
        <GraphView vars={vars} bindings={bindings} />
      </Suspense>,
    )
  }

  destroy() {
    this.root?.unmount()
    this.container?.remove()
    this.container = undefined
    this.root = undefined
  }
}

let registered = false

/**
 * `@zazuko/yasgui` ships as a single pre-bundled `yasgui.min.js` with its own
 * inlined copy of the `Yasr` class, distinct from the `@zazuko/yasr` package's
 * module instance. Registering against the latter is invisible to Yasgui at
 * runtime, so this must go through the class Yasgui itself exposes.
 *
 * Idempotent: registerPlugin is a static side effect, safe to call once per module lifetime.
 */
export function registerGraphPlugin(yasgui: typeof Yasgui) {
  if (registered) return
  registered = true
  // @zazuko/yasr's own .d.ts types `registerPlugin`'s second parameter against the
  // DOM lib's ambient `Plugin` (navigator plugins) rather than its own `Plugin`
  // interface — the same mistake Table/Boolean/etc. hit, so this cast matches theirs.
  yasgui.Yasr.registerPlugin('graph', GraphPlugin as unknown as Parameters<typeof yasgui.Yasr.registerPlugin>[1])
}
