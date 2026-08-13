import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import Yasgui from '@zazuko/yasgui'
import '@zazuko/yasgui/build/yasgui.min.css'
import '../yasgui-catppuccin.css'
import { ShareLinkButton } from '../components/ShareLinkButton'
import { registerGraphPlugin } from './sparql/GraphPlugin'
import { setQueryRunner, setQueryTextGetter } from './sparql/sparqlBridge'

registerGraphPlugin(Yasgui)

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
    query: `${PREFIXES}
SELECT DISTINCT ?property
WHERE {
  ?property rdf:type rdf:Property .
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
      setQueryRunner(null)
      setQueryTextGetter(null)
      yasgui.current = null
      instance.destroy()
      parent.replaceChildren()
    }
  }, [sharedQuery, shareHost])

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
        </header>
        <div className="chips">
          {SAMPLES.map((sample) => (
            <button key={sample.name} type="button" className="chip" onClick={() => loadSample(sample)}>
              {sample.name}
            </button>
          ))}
        </div>
        <div className="yasgui-host" ref={container} />
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
