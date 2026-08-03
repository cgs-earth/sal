export function MapTab() {
  return (
    <div className="tab-body map">
      <section className="panel map-panel">
        <header className="panel-header">
          <h3>Map</h3>
          <p>
            Renders the <code>object_geometry</code> column of the triples table.
          </p>
        </header>
        <div className="map-placeholder">
          <span className="map-glyph" aria-hidden="true">
            ◍
          </span>
          <h4>Not wired up yet</h4>
          {/* TODO: mount MapLibre GL here and page through /geometries?limit=&offset=,
              rendering points, lines, and polygons with a popup per feature. The Go
              endpoint already returns GeoJSON; only the client is missing. */}
          <p>
            TODO: mount a MapLibre map against the <code>/geometries</code> endpoint, which already serves the
            table&rsquo;s geometries as GeoJSON. Left blank until there is geospatial data to test against.
          </p>
        </div>
      </section>
    </div>
  )
}
