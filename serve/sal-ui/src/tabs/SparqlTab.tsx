import { useEffect, useRef } from 'react'
import Yasgui from '@zazuko/yasgui'
import '@zazuko/yasgui/build/yasgui.min.css'
import '../yasgui-catppuccin.css'

const DEFAULT_QUERY = `PREFIX schema: <https://schema.org/>

SELECT ?s ?p ?o
WHERE {
  ?s ?p ?o .
}`

export function SparqlTab() {
  const container = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const parent = container.current
    if (!parent) return

    const endpoint = `${window.location.origin}/sparql`
    const yasgui = new Yasgui(parent, {
      requestConfig: { endpoint, method: 'POST' },
      copyEndpointOnNewTab: true,
    })
    // Seed a first-run tab; a restored tab keeps whatever the user last typed.
    const tab = yasgui.getTab()
    if (tab && tab.getQuery().trim() === '') {
      tab.setQuery(DEFAULT_QUERY)
    }

    return () => {
      yasgui.destroy()
      parent.replaceChildren()
    }
  }, [])

  return (
    <div className="tab-body sparql">
      <section className="panel yasgui-panel">
        <header className="panel-header">
          <h3>SPARQL</h3>
          <p>
            Queries run against the local <code>/sparql</code> endpoint, which translates SPARQL to DuckDB SQL.
          </p>
        </header>
        <div className="yasgui-host" ref={container} />
      </section>
    </div>
  )
}
