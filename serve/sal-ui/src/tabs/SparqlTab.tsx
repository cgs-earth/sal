import { useCallback, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import Yasgui from '@zazuko/yasgui'
import '@zazuko/yasgui/build/yasgui.min.css'
import '../yasgui-catppuccin.css'
import { translateSparql } from '../api'
import { ShareLinkButton } from '../components/ShareLinkButton'
import { registerGraphPlugin } from './sparql/GraphPlugin'
import { setQueryRunner, setQueryTextGetter } from './sparql/sparqlBridge'
import { SqlPreview, type Translation } from './sparql/SqlPreview'
import { publishResult } from '../results'

registerGraphPlugin(Yasgui)

/** Where the SQL pane's on/off choice is remembered between visits. */
const SHOW_SQL_KEY = 'sal-ui.sparql.showSql'

function readShowSql(): boolean {
  try {
    return localStorage.getItem(SHOW_SQL_KEY) === 'true'
  } catch {
    return false
  }
}

/** How long typing has to pause before the query is sent for translation. */
const TRANSLATE_DEBOUNCE_MS = 250

const PREFIXES = `PREFIX rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#>
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
`

type Sample = { name: string; query: string }

/*
 * Starter queries. The `/sparql` endpoint translates SPARQL to DuckDB SQL and
 * only understands basic triple patterns, FILTER comparisons, DISTINCT and
 * LIMIT, so every sample stays inside that subset.
 */
const SAMPLES: Sample[] = [
  {
    name: 'All triples',
    query: `SELECT ?subject ?predicate ?object
WHERE {
  ?subject ?predicate ?object .
}
LIMIT 20`,
  },
  {
    name: 'Classes',
    query: `${PREFIXES}
SELECT DISTINCT ?class
WHERE {
  ?class rdf:type rdfs:Class .
}`,
  },
  {
    name: 'Types in use',
    query: `${PREFIXES}
SELECT DISTINCT ?type
WHERE {
  ?subject rdf:type ?type .
}`,
  },
  {
    name: 'Properties',
    query: `PREFIX rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#>
PREFIX owl: <http://www.w3.org/2002/07/owl#>

SELECT ?property ?type
WHERE {
  ?property rdf:type ?type .

  FILTER (
    ?type = rdf:Property ||
    ?type = owl:ObjectProperty ||
    ?type = owl:DatatypeProperty ||
    ?type = owl:AnnotationProperty
  )
}`,
  },
  {
    name: 'Predicates',
    query: `SELECT DISTINCT ?predicate
WHERE {
  ?subject ?predicate ?object .
}`,
  },
  {
    name: 'Subclasses',
    query: `${PREFIXES}
SELECT ?class ?parent
WHERE {
  ?class rdfs:subClassOf ?parent .
}`,
  },
  {
    name: 'Labels',
    query: `${PREFIXES}
SELECT ?subject ?label
WHERE {
  ?subject rdfs:label ?label .
}
LIMIT 50`,
  },
  {
    name: 'Typed labels',
    query: `${PREFIXES}
SELECT ?subject ?type ?label
WHERE {
  ?subject rdf:type ?type .
  ?subject rdfs:label ?label .
}
LIMIT 50`,
  },
  {
    name: 'Instances only',
    query: `${PREFIXES}
SELECT ?subject ?type
WHERE {
  ?subject rdf:type ?type .
  FILTER(?type != rdfs:Class)
}
LIMIT 50`,
  },
  {
    name: 'Geometries',
    query: `PREFIX geo: <http://www.opengis.net/ont/geosparql#>

SELECT ?feature ?wkt
WHERE {
  ?feature geo:hasGeometry ?geometry .
  ?geometry geo:asWKT ?wkt .
}
LIMIT 500`,
  },
  {
    name: 'Ontology versions',
    query: `PREFIX rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#>
PREFIX owl: <http://www.w3.org/2002/07/owl#>
PREFIX dcterms: <http://purl.org/dc/terms/>

SELECT ?ontology ?versionIRI ?format ?modified
WHERE {
  ?ontology rdf:type owl:Ontology .
  ?ontology owl:versionIRI ?versionIRI .
  ?ontology dcterms:format ?format .
  ?ontology dcterms:modified ?modified .
}`,
  },
]

export function SparqlTab({ sharedQuery }: { sharedQuery: string | null }) {
  const container = useRef<HTMLDivElement>(null)
  const yasgui = useRef<Yasgui | null>(null)
  // Yasr builds a results header per Yasgui tab, so the share button lives in a
  // node of this component's own that is moved into whichever header is on
  // screen. Portaling straight into the header instead would leave React
  // unmounting children out of a node Yasgui had already torn down.
  const [shareHost] = useState(() => document.createElement('div'))

  // The SQL pane is a debugging aid, so it is off until asked for, and the
  // choice sticks. Yasgui's own handlers read it through a ref so that turning
  // it on or off never rebuilds the editor.
  const [showSql, setShowSql] = useState(readShowSql)
  const showSqlRef = useRef(showSql)
  showSqlRef.current = showSql
  const [translation, setTranslation] = useState<Translation>({ status: 'empty' })
  const [editorHeight, setEditorHeight] = useState<number | null>(null)
  const translateTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const translateAbort = useRef<AbortController | null>(null)

  // Translates whatever the active Yasgui tab holds, debounced so that typing
  // does not send a request per keystroke and an older request never lands
  // after a newer one.
  const scheduleTranslation = useCallback(() => {
    if (!showSqlRef.current) return
    if (translateTimer.current) clearTimeout(translateTimer.current)
    translateTimer.current = setTimeout(() => {
      translateTimer.current = null
      translateAbort.current?.abort()
      const query = yasgui.current?.getTab()?.getQuery() ?? ''
      if (!query.trim()) {
        setTranslation({ status: 'empty' })
        return
      }
      const controller = new AbortController()
      translateAbort.current = controller
      translateSparql(query, controller.signal).then(
        (sql) => {
          if (!controller.signal.aborted) setTranslation({ status: 'ok', sql })
        },
        (caught: unknown) => {
          if (controller.signal.aborted) return
          setTranslation({ status: 'error', message: caught instanceof Error ? caught.message : String(caught) })
        },
      )
    }, TRANSLATE_DEBOUNCE_MS)
  }, [])

  const toggleShowSql = (next: boolean) => {
    setShowSql(next)
    showSqlRef.current = next
    try {
      localStorage.setItem(SHOW_SQL_KEY, String(next))
    } catch {
      // Storage may be unavailable; the pane still works for this visit.
    }
    if (next) scheduleTranslation()
  }

  useEffect(() => {
    const parent = container.current
    if (!parent) return

    const endpoint = `${window.location.origin}/sparql`
    const instance = new Yasgui(parent, {
      requestConfig: { endpoint, method: 'POST' },
      copyEndpointOnNewTab: true,
    })
    yasgui.current = instance
    // Seed a first-run tab; a restored tab keeps whatever the user last typed.
    const tab = instance.getTab()
    if (tab && tab.getQuery().trim() === '') {
      tab.setQuery(SAMPLES[0].query)
      tab.setName(SAMPLES[0].name)
    }
    // A shared query opens in a tab of its own rather than overwriting whatever
    // the browser restored; avoidDuplicateTabs keeps reopening the same link
    // from stacking up copies of it.
    if (sharedQuery) {
      instance.addTab(true, { name: 'Shared query', yasqe: { value: sharedQuery } }, { avoidDuplicateTabs: true })
    }

    // appendChild moves the node, so the button follows the selected tab.
    shareHost.className = 'yasr-share-host'
    let disposed = false
    const syncShareHost = () => {
      // tabAdd fires before Yasgui has built the new tab and made it active, so
      // the selection is read back a microtask later rather than during the event.
      queueMicrotask(() => {
        if (disposed) return
        instance.getTab()?.getYasr()?.headerEl.appendChild(shareHost)
      })
    }
    syncShareHost()
    instance.on('tabSelect', syncShareHost)
    instance.on('tabAdd', syncShareHost)
    instance.on('tabClose', syncShareHost)

    // The SQL pane follows the active tab's query, and matches the height of
    // its editor so the two line up when they sit side by side. Yasgui emits
    // tabChange for every edit of a tab's query, and tabSelect when another
    // tab's query comes on screen; the editor element is read back a microtask
    // later for the same reason the share host is.
    const editorObserver = new ResizeObserver((entries) => {
      const entry = entries[0]
      if (entry) setEditorHeight(Math.round(entry.contentRect.height))
    })
    const followActiveTab = () => {
      queueMicrotask(() => {
        if (disposed) return
        editorObserver.disconnect()
        const editor = instance.getTab()?.getYasqe()?.getWrapperElement()
        if (editor) editorObserver.observe(editor)
        scheduleTranslation()
      })
    }
    followActiveTab()
    instance.on('tabSelect', followActiveTab)
    instance.on('tabAdd', followActiveTab)
    instance.on('tabChange', scheduleTranslation)

    // The Map tab draws whatever geometry the last result held. Yasgui emits
    // queryResponse before Yasr has stored the result, so it is read a
    // microtask later.
    instance.on('queryResponse', (_, tab) => {
      queueMicrotask(() => {
        if (disposed) return
        const results = tab.getYasr()?.results
        if (!results || results.hasError()) return
        const vars = results.getVariables()
        const rows = results.getBindings().map((binding) => vars.map((name) => binding[name]?.value ?? ''))
        publishResult({ source: 'SPARQL', query: tab.getQuery(), header: vars, rows })
      })
    })

    setQueryRunner((query) => {
      const activeTab = instance.getTab()
      if (!activeTab) return
      activeTab.setQuery(query)
      activeTab.setName('From graph')
      void activeTab.query()
    })
    setQueryTextGetter(() => instance.getTab()?.getQuery() ?? '')

    return () => {
      disposed = true
      editorObserver.disconnect()
      if (translateTimer.current) clearTimeout(translateTimer.current)
      translateAbort.current?.abort()
      setQueryRunner(null)
      setQueryTextGetter(null)
      yasgui.current = null
      instance.destroy()
      parent.replaceChildren()
    }
  }, [sharedQuery, shareHost, scheduleTranslation])

  // Samples load into the active tab, which is also renamed so the tab strip
  // reads as the query it holds rather than "Query 1".
  const loadSample = (sample: Sample) => {
    const tab = yasgui.current?.getTab()
    if (!tab) return
    tab.setQuery(sample.query)
    tab.setName(sample.name)
  }

  return (
    <div className="tab-body sparql">
      <section className="panel yasgui-panel">
        <header className="panel-header">
          <h3>SPARQL</h3>
          <p>
            Queries run against the local <code>/sparql</code> endpoint, which translates SPARQL to DuckDB SQL.
          </p>
          <div className="panel-header-actions">
            <label className="toggle sql-toggle" title="Show the DuckDB SQL the query translates to">
              <input type="checkbox" checked={showSql} onChange={(event) => toggleShowSql(event.target.checked)} />
              Show SQL
            </label>
          </div>
        </header>
        <div className="chips">
          {SAMPLES.map((sample) => (
            <button key={sample.name} type="button" className="chip" onClick={() => loadSample(sample)}>
              {sample.name}
            </button>
          ))}
        </div>
        {/* The host stays mounted whether or not the SQL pane is shown, since
            the Yasgui instance lives in it. With the pane on, the workspace
            puts the two side by side when it is wide enough and stacks them
            otherwise; see .sparql-workspace in App.css. */}
        <div className={`sparql-workspace${showSql ? ' with-sql' : ''}`}>
          <div className="yasgui-host" ref={container} />
          {showSql && <SqlPreview translation={translation} height={editorHeight} />}
        </div>
        {/* The host is appended last to Yasr's header, so Yasr's own flex spacer
            pushes it to the right of the Table/Graph/Response plugin buttons. */}
        {createPortal(
          <ShareLinkButton
            tab="SPARQL"
            className="button yasr-share"
            query={() => yasgui.current?.getTab()?.getQuery() ?? ''}
          />,
          shareHost,
        )}
      </section>
    </div>
  )
}
