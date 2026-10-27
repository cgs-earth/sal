import { useCallback, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import Yasgui from '@zazuko/yasgui'
import '@zazuko/yasgui/build/yasgui.min.css'
import '../yasgui-theme.css'
import { fetchVocabularies, translateSparql, type QueryResult, type Vocabulary } from '../api'
import { ShareLinkButton } from '../components/ShareLinkButton'
import { SampleQueryPicker } from '../components/SampleQueryPicker'
import { PrefixPicker, type Prefixes } from '../components/PrefixPicker'
import { registerGraphPlugin } from './sparql/GraphPlugin'
import { getVocabularies, registerSalCompleters, setVocabularies, type TermInfo } from './sparql/completers'
import { setQueryRunner, setQueryTextGetter } from './sparql/sparqlBridge'
import { termAtCaret, watchCaretTerm } from './sparql/caretTerm'
import { TermDefinitions } from './sparql/TermDefinitions'
import { SqlPreview, type Translation } from './sparql/SqlPreview'
import { publishResult } from '../results'

registerGraphPlugin(Yasgui)
// Before the first Yasgui is built, since it decides which completers a new
// editor enables.
registerSalCompleters(Yasgui)

/** Where the SQL pane's on/off choice is remembered between visits. */
const SHOW_SQL_KEY = 'sal-ui.sparql.showSql'
/** Where the Definitions pane's on/off choice is remembered; it is on until turned off. */
const SHOW_DEFINITIONS_KEY = 'sal-ui.sparql.showDefinitions'

function readShowSql(): boolean {
  try {
    return localStorage.getItem(SHOW_SQL_KEY) === 'true'
  } catch {
    return false
  }
}

function readShowDefinitions(): boolean {
  try {
    return localStorage.getItem(SHOW_DEFINITIONS_KEY) !== 'false'
  } catch {
    return true
  }
}

/** The class on a Yasqe root that narrows its editor to make room for the Definitions pane. */
const WITH_DEFINITIONS_CLASS = 'sal-with-definitions'

type Yasqe = InstanceType<typeof Yasgui.Yasqe>

/** How long typing has to pause before the query is sent for translation. */
const TRANSLATE_DEBOUNCE_MS = 250

const PREFIXES = `PREFIX rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#>
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
`

type Sample = { name: string; query: string }

/*
 * Starter queries. The `/sparql` endpoint translates SPARQL to DuckDB SQL and
 * understands triple patterns, property paths, OPTIONAL, UNION, BIND, VALUES,
 * subqueries, FILTER expressions and the SPARQL builtins, aggregates with
 * GROUP BY and HAVING, ORDER BY, LIMIT and OFFSET, MINUS and FILTER [NOT]
 * EXISTS, ASK, and a SERVICE naming one of this server's own snapshot
 * endpoints, so every sample stays inside that subset. The snapshot samples
 * need a snapshot ID only the server knows, so they are built by
 * snapshotSamples from the Stats listing rather than listed here. The
 * samples name no term of any particular vocabulary beyond rdf, rdfs, and
 * the ones sal itself writes, so they work over whatever the dataset holds;
 * the inherited classes and label samples walk whatever class and property
 * hierarchies the table holds, the project's own statements and the
 * ontologies it imports with owl:imports.
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
    name: 'Inherited classes in use',
    query: `${PREFIXES}
SELECT DISTINCT ?class
WHERE {
  ?subject rdf:type/rdfs:subClassOf+ ?class .
}`,
  },
  {
    name: 'Labels through subproperties',
    query: `${PREFIXES}
SELECT ?subject ?label
WHERE {
  ?subject ?property ?label .
  ?property rdfs:subPropertyOf+ rdfs:label .
}
LIMIT 50`,
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
    name: 'Labels by language',
    query: `${PREFIXES}
SELECT ?subject ?label (LANG(?label) AS ?lang)
WHERE {
  ?subject rdfs:label ?label .
  FILTER(langMatches(LANG(?label), "en"))
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
    name: 'Instances per type',
    query: `${PREFIXES}
# How many subjects each type has, largest first: an aggregate with
# GROUP BY, HAVING, and ORDER BY.
SELECT ?type (COUNT(?subject) AS ?instances)
WHERE {
  ?subject rdf:type ?type .
}
GROUP BY ?type
HAVING (COUNT(?subject) > 1)
ORDER BY DESC(?instances)
LIMIT 25`,
  },
  {
    name: 'Predicate usage',
    query: `# Every predicate with how often it is used and how many distinct
# subjects use it.
SELECT ?predicate (COUNT(*) AS ?uses) (COUNT(DISTINCT ?subject) AS ?subjects)
WHERE {
  ?subject ?predicate ?object .
}
GROUP BY ?predicate
ORDER BY DESC(?uses) ?predicate`,
  },
  {
    name: 'Labels where available',
    query: `${PREFIXES}
# Every typed subject, with its label when it has one and its IRI
# otherwise: OPTIONAL leaves ?label unbound, and COALESCE fills it in.
SELECT ?subject ?type (COALESCE(?label, STR(?subject)) AS ?name)
WHERE {
  ?subject rdf:type ?type .
  OPTIONAL { ?subject rdfs:label ?label }
}
ORDER BY ?type ?name
LIMIT 50`,
  },
  {
    name: 'Unlabelled subjects',
    query: `${PREFIXES}
# Typed subjects that carry no rdfs:label, found by an OPTIONAL that
# matched nothing. FILTER NOT EXISTS asks the same question.
SELECT ?subject ?type
WHERE {
  ?subject rdf:type ?type .
  OPTIONAL { ?subject rdfs:label ?label }
  FILTER(!BOUND(?label))
}
LIMIT 50`,
  },
  {
    name: 'Labels or comments',
    query: `${PREFIXES}
# One column of text from either property, with the property it came
# from: each arm of the UNION binds ?text and ?source its own way.
SELECT ?subject ?text ?source
WHERE {
  { ?subject rdfs:label ?text . BIND("label" AS ?source) }
  UNION
  { ?subject rdfs:comment ?text . BIND("comment" AS ?source) }
}
ORDER BY ?subject ?source
LIMIT 50`,
  },
  {
    name: 'Describe a few subjects',
    query: `# Everything said about a handful of subjects named inline. Replace
# the IRIs with ones from the All triples sample.
SELECT ?subject ?predicate ?object
WHERE {
  VALUES ?subject { <https://example.org/a> <https://example.org/b> }
  ?subject ?predicate ?object .
}
ORDER BY ?subject ?predicate`,
  },
  {
    name: 'Search labels',
    query: `${PREFIXES}
# Case-insensitive search through labels with REGEX, projecting the
# label's length and an upper-cased copy through BIND.
SELECT ?subject ?label ?length ?shout
WHERE {
  ?subject rdfs:label ?label .
  FILTER(REGEX(?label, "a", "i"))
  BIND(STRLEN(?label) AS ?length)
  BIND(UCASE(?label) AS ?shout)
}
ORDER BY DESC(?length)
LIMIT 50`,
  },
  {
    name: 'Numeric objects',
    query: `# Every numeric value in the table, largest first, with a little
# arithmetic: isNumeric reads the typed object columns.
SELECT ?subject ?predicate ?value (?value * 2 AS ?doubled)
WHERE {
  ?subject ?predicate ?value .
  FILTER(isNumeric(?value))
}
ORDER BY DESC(?value)
LIMIT 50`,
  },
  {
    name: 'Most described subjects',
    query: `${PREFIXES}
# The subjects with the most statements, joined to their labels through
# a subquery that counts per subject.
SELECT ?subject ?statements ?label
WHERE {
  {
    SELECT ?subject (COUNT(*) AS ?statements)
    WHERE { ?subject ?predicate ?object . }
    GROUP BY ?subject
  }
  OPTIONAL { ?subject rdfs:label ?label }
}
ORDER BY DESC(?statements)
LIMIT 25`,
  },
  {
    name: 'Second page of labels',
    query: `${PREFIXES}
# Page through an ordered result with LIMIT and OFFSET.
SELECT ?subject ?label
WHERE {
  ?subject rdfs:label ?label .
}
ORDER BY ?label
LIMIT 25
OFFSET 25`,
  },
  {
    name: 'Any labels?',
    query: `${PREFIXES}
# An ASK answers true or false rather than rows.
ASK {
  ?subject rdfs:label ?label .
}`,
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
  {
    name: 'Pinned blobs',
    query: `PREFIX owl: <http://www.w3.org/2002/07/owl#>
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>

# Every blob the table refers to, the same listing the Blobs tab shows:
# a pinned vocabulary is labelled with its namespace, and a file or
# directory a SAL module task handed over with the file:/// IRI the task
# named it by. Paste a hash into the Blobs tab to download it.
SELECT ?name ?hash
WHERE {
  ?blob owl:versionIRI ?hash .
  ?blob rdfs:label ?name .
}
LIMIT 100`,
  },
]

/**
 * The samples diffing the table against its previous snapshot: the triples
 * the table has that the snapshot lacks, and the reverse, each a MINUS over a
 * SERVICE on the versioned endpoint. A join could not answer this, since it
 * would pair every value of a multi-valued predicate with every other. The
 * endpoint is written with this page's own origin so the samples run wherever
 * the server is reached from. A table with a single snapshot is diffed against
 * that snapshot itself, which answers no rows but still shows the shape; with
 * no snapshot listing there is no ID to write, so there are no samples.
 */
function snapshotSamples(snapshots: QueryResult | null): Sample[] {
  const column = snapshots?.header?.indexOf('snapshot_id') ?? -1
  if (column < 0) return []
  // The Stats listing is ordered newest first.
  const ids = (snapshots?.rows ?? []).map((row) => row[column] ?? '').filter((id) => id !== '')
  const previous = ids[1] ?? ids[0]
  if (!previous) return []
  const snapshot = `SERVICE <${window.location.origin}/v${previous}/sparql> {
    ?subject ?predicate ?object .
  }`
  return [
    {
      name: 'Added since previous snapshot',
      query: `SELECT ?subject ?predicate ?object
WHERE {
  ?subject ?predicate ?object .
  MINUS {
    ${snapshot.replace(/\n/g, '\n  ')}
  }
}
LIMIT 50`,
    },
    {
      name: 'Removed since previous snapshot',
      query: `SELECT ?subject ?predicate ?object
WHERE {
  ${snapshot}
  MINUS {
    ?subject ?predicate ?object .
  }
}
LIMIT 50`,
    },
  ]
}

export function SparqlTab({ sharedQuery, snapshots }: { sharedQuery: string | null; snapshots: QueryResult | null }) {
  const samples = [...SAMPLES, ...snapshotSamples(snapshots)]
  const container = useRef<HTMLDivElement>(null)
  const yasgui = useRef<Yasgui | null>(null)
  // Yasr builds a results header per Yasgui tab, so the share button lives in a
  // node of this component's own that is moved into whichever header is on
  // screen. Portaling straight into the header instead would leave React
  // unmounting children out of a node Yasgui had already torn down.
  const [shareHost] = useState(() => document.createElement('div'))
  // The Definitions pane sits inside the editor of whichever Yasgui tab is on
  // screen, so like the share button it is a node of this component's own
  // that is moved into the active editor rather than portaled straight in.
  const [definitionsHost] = useState(() => document.createElement('aside'))
  const [showDefinitions, setShowDefinitions] = useState(readShowDefinitions)
  const showDefinitionsRef = useRef(showDefinitions)
  showDefinitionsRef.current = showDefinitions
  const [caretTerm, setCaretTerm] = useState<TermInfo | null>(null)

  // The SQL pane is a debugging aid, so it is off until asked for, and the
  // choice sticks. Yasgui's own handlers read it through a ref so that turning
  // it on or off never rebuilds the editor.
  const [showSql, setShowSql] = useState(readShowSql)
  const showSqlRef = useRef(showSql)
  showSqlRef.current = showSql
  const [translation, setTranslation] = useState<Translation>({ status: 'empty' })
  // The help note is closed until asked for and not remembered, since it only
  // explains the header's controls.
  const [showHelp, setShowHelp] = useState(false)
  const [editorHeight, setEditorHeight] = useState<number | null>(null)
  const translateTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const translateAbort = useRef<AbortController | null>(null)

  // The vocabularies the project pins feed both the Prefixes dialog and the
  // editor's completers, which read them from the module-level store. They
  // are read once per visit to the tab; the server parses each pinned
  // document once and answers the rest from memory.
  const [vocabularies, setVocabularyList] = useState<Vocabulary[] | null>(null)
  const [vocabularyError, setVocabularyError] = useState<string | null>(null)
  useEffect(() => {
    const controller = new AbortController()
    fetchVocabularies(controller.signal).then(
      (listing) => {
        setVocabularies(listing.vocabularies)
        setVocabularyList(getVocabularies())
      },
      (caught: unknown) => {
        if (controller.signal.aborted) return
        setVocabularyError(caught instanceof Error ? caught.message : String(caught))
        setVocabularyList([])
      },
    )
    return () => controller.abort()
  }, [])

  const declaredPrefixes = useCallback((): Prefixes => yasgui.current?.getTab()?.getYasqe()?.getPrefixesFromQuery() ?? {}, [])
  const insertPrefixes = (prefixes: Prefixes) => {
    const yasqe = yasgui.current?.getTab()?.getYasqe()
    if (!yasqe) return
    yasqe.addPrefixes(prefixes)
    scheduleTranslation()
  }

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

  const toggleShowDefinitions = (next: boolean) => {
    setShowDefinitions(next)
    showDefinitionsRef.current = next
    try {
      localStorage.setItem(SHOW_DEFINITIONS_KEY, String(next))
    } catch {
      // Storage may be unavailable; the pane still follows the box for this visit.
    }
    const yasqe = yasgui.current?.getTab()?.getYasqe()
    if (yasqe) dockDefinitions(yasqe)
  }

  /**
   * Puts the Definitions pane into yasqe's editor, narrowing it, or takes it
   * out, as the box says. CodeMirror measures its width when asked, so it is
   * refreshed once the margin has changed.
   */
  const dockDefinitions = useCallback(
    (yasqe: Yasqe) => {
      if (showDefinitionsRef.current) {
        yasqe.rootEl.classList.add(WITH_DEFINITIONS_CLASS)
        yasqe.rootEl.appendChild(definitionsHost)
        setCaretTerm(termAtCaret(yasqe))
      } else {
        yasqe.rootEl.classList.remove(WITH_DEFINITIONS_CLASS)
        definitionsHost.remove()
      }
      yasqe.refresh()
    },
    [definitionsHost],
  )

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
      // Share links are this app's own (`q`, read by routing.ts), so Yasgui
      // must not read the address bar itself. Left on, it treats every
      // parameter it does not know as a request argument, and would send `q`
      // along to the endpoint with every query.
      populateFromUrl: false,
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
    // later for the same reason the share host is. The Definitions pane is
    // docked into each editor as it comes on screen the same way, and the
    // caret of every editor is watched from then on, once each.
    const editorObserver = new ResizeObserver((entries) => {
      const entry = entries[0]
      if (entry) setEditorHeight(Math.round(entry.contentRect.height))
    })
    const caretWatchers = new Map<object, () => void>()
    const followActiveTab = () => {
      queueMicrotask(() => {
        if (disposed) return
        editorObserver.disconnect()
        const yasqe = instance.getTab()?.getYasqe()
        if (yasqe) {
          editorObserver.observe(yasqe.getWrapperElement())
          definitionsHost.className = 'sal-term-panel'
          dockDefinitions(yasqe)
          if (!caretWatchers.has(yasqe)) caretWatchers.set(yasqe, watchCaretTerm(yasqe, setCaretTerm))
        }
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
      for (const stop of caretWatchers.values()) stop()
      definitionsHost.remove()
      if (translateTimer.current) clearTimeout(translateTimer.current)
      translateAbort.current?.abort()
      setQueryRunner(null)
      setQueryTextGetter(null)
      yasgui.current = null
      instance.destroy()
      parent.replaceChildren()
    }
  }, [sharedQuery, shareHost, definitionsHost, dockDefinitions, scheduleTranslation])

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
          <div className="panel-header-actions">
            <SampleQueryPicker
              compact
              samples={samples.map((sample) => ({ name: sample.name, text: sample.query }))}
              onPick={(sample) => loadSample({ name: sample.name, query: sample.text })}
            />
            <PrefixPicker
              compact
              vocabularies={vocabularies}
              error={vocabularyError}
              declared={declaredPrefixes}
              onInsert={insertPrefixes}
            />
            <label
              className="toggle definitions-toggle"
              title="Show what the pinned vocabulary says about the term under the caret"
            >
              <input
                type="checkbox"
                checked={showDefinitions}
                onChange={(event) => toggleShowDefinitions(event.target.checked)}
              />
              Show RDF Definitions
            </label>
            <label className="toggle sql-toggle" title="Show the DuckDB SQL the query translates to">
              <input type="checkbox" checked={showSql} onChange={(event) => toggleShowSql(event.target.checked)} />
              Show SQL
            </label>
            <button
              type="button"
              className={`button small help-button${showHelp ? ' active' : ''}`}
              aria-label={showHelp ? 'Hide help' : 'Show help'}
              aria-expanded={showHelp}
              title="What this tab does"
              onClick={() => setShowHelp((open) => !open)}
            >
              ?
            </button>
          </div>
        </header>
        {showHelp && (
          <div className="help-panel" role="note">
            <ul>
              <li>
                Queries run against the local <code>/sparql</code> endpoint, which translates SPARQL to DuckDB SQL rather
                than evaluating it in a triplestore. <strong>Show SQL</strong> opens that translation beside the editor.
              </li>
              <li>
                <strong>Prefixes</strong> lists the vocabularies the project pins, the ones the Stats tab shows, and adds
                the <code>PREFIX</code> line of any of them to the query. Once a prefix is declared, typing it and a colon
                (<code>schema:na</code>) suggests the terms its pinned document defines, so what is offered is what the
                build validated against; <kbd>Ctrl</kbd>+<kbd>Space</kbd> asks for the list by hand. A prefix typed
                without a declaration is declared for you when it names one of those vocabularies.
              </li>
              <li>
                <strong>Show RDF Definitions</strong> keeps a pane down the right of the editor describing the term
                the caret is on, or the suggestion highlighted in the completion list: its IRI, whether it is a class
                or a property, and what its pinned document states about it (<code>rdfs:comment</code>,{' '}
                <code>skos:definition</code>, <code>rdfs:subClassOf</code>, <code>rdfs:domain</code>, and the like).
                Untick it to give the editor its full width back.
              </li>
              <li>
                A <code>SERVICE</code> naming <code>/v&#123;snapshot&#125;/sparql</code> reads the table at an earlier
                snapshot, and <code>MINUS</code> subtracts one from the other, so one query can diff two.
              </li>
              <li>
                <code>OPTIONAL</code>, <code>UNION</code>, <code>BIND</code>, <code>VALUES</code>, subqueries,
                aggregates with <code>GROUP BY</code> and <code>HAVING</code>, <code>ORDER BY</code> with{' '}
                <code>OFFSET</code>, the string, numeric, and date builtins, and <code>ASK</code> all translate; the{' '}
                <em>Instances per type</em>, <em>Labels where available</em>, and <em>Labels or comments</em> samples
                show the shape. <code>GRAPH</code> does not, since the table holds a single graph.
              </li>
              <li>
                A property path such as <code>rdfs:subClassOf*</code> walks whatever hierarchy the table holds: the
                project&apos;s own statements and the ontologies it imports with <code>owl:imports</code>. A vocabulary
                the project only validates against is not in the table, so nothing is inferred from it. The{' '}
                <em>Inherited classes in use</em> and <em>Labels through subproperties</em> samples are written for this.
              </li>
            </ul>
          </div>
        )}
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
        {createPortal(<TermDefinitions term={caretTerm} />, definitionsHost)}
        {createPortal(
          <ShareLinkButton
            tab="SPARQL"
            className="yasr-share"
            query={() => yasgui.current?.getTab()?.getQuery() ?? ''}
          />,
          shareHost,
        )}
      </section>
    </div>
  )
}
