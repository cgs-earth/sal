import { useEffect, useRef } from 'react'
import Yasgui from '@zazuko/yasgui'
import '@zazuko/yasgui/build/yasgui.min.css'
import '../yasgui-catppuccin.css'
import { registerGraphPlugin } from './sparql/GraphPlugin'

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

export function SparqlTab() {
  const container = useRef<HTMLDivElement>(null)
  const yasgui = useRef<Yasgui | null>(null)

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

    return () => {
      yasgui.current = null
      instance.destroy()
      parent.replaceChildren()
    }
  }, [])

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
      </section>
    </div>
  )
}
