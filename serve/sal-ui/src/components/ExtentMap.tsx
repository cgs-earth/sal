import { useEffect, useRef, useState } from 'react'
import type { GeoJSONSource } from 'maplibre-gl'
import { fetchExtent, type BBox } from '../api'
import { addExtentLayers, EXTENT_SOURCE } from '../basemap'
import { useMapLibre } from './useMapLibre'

type ExtentMapProps = {
  /** Changes whenever the stats are reloaded, so the extent is read again with them. */
  refreshKey: unknown
}

/**
 * The small map on the Stats card: the table's spatial extent as a dotted
 * outline over a basemap, zoomable and nothing more. The Geospatial tab is where
 * geometries are queried; this only says where the data is.
 */
export function ExtentMap({ refreshKey }: ExtentMapProps) {
  const container = useRef<HTMLDivElement>(null)
  const [bbox, setBBox] = useState<BBox | null>(null)
  const [empty, setEmpty] = useState(false)
  // No chrome: the zoom buttons and the attribution control would take a
  // third of a map this size. It still zooms by wheel and double click, and
  // the basemap credit is on the caption; the Geospatial tab shows it in full. One
  // world, too: an extent spanning the globe would otherwise be drawn on every
  // copy, and a map this small shows two of them.
  const { map: mapRef, styleVersion } = useMapLibre(container, {
    addLayers: (map, palette) => addExtentLayers(map, palette.extent),
    controls: false,
    mapOptions: { zoom: 0.6, renderWorldCopies: false, attributionControl: false },
  })

  useEffect(() => {
    const controller = new AbortController()
    fetchExtent(controller.signal)
      .then((feature) => {
        setBBox(feature.bbox ?? null)
        setEmpty(!feature.bbox)
      })
      .catch((caught) => {
        if (!controller.signal.aborted) {
          console.error(caught)
          setEmpty(true)
        }
      })
    return () => controller.abort()
  }, [refreshKey])

  // Draws the extent and fits the view to it once both the style and the
  // extent have arrived, and again after a theme switch rebuilds the layers.
  useEffect(() => {
    const map = mapRef.current
    if (styleVersion === 0 || !map) return
    const [west, south, east, north] = bbox ?? [0, 0, 0, 0]
    const source = map.getSource(EXTENT_SOURCE) as GeoJSONSource | undefined
    source?.setData(
      bbox
        ? {
            type: 'Feature',
            properties: {},
            geometry: {
              type: 'Polygon',
              coordinates: [
                [
                  [west, south],
                  [east, south],
                  [east, north],
                  [west, north],
                  [west, south],
                ],
              ],
            },
          }
        : { type: 'FeatureCollection', features: [] },
    )
    if (!bbox) return
    if (east - west >= 300) {
      // An extent spanning the globe: with world copies off, MapLibre never
      // lets one world be narrower than the map, so fitting the box's height
      // would be clamped anyway. Zooming out as far as it allows shows the
      // world once across the width, with the outline on the frame.
      map.jumpTo({ center: [(west + east) / 2, (south + north) / 2], zoom: map.getMinZoom() })
      return
    }
    map.fitBounds([west, south, east, north], { padding: 16, duration: 0, maxZoom: 12 })
  }, [mapRef, styleVersion, bbox])

  return (
    <figure className="dataset-extent">
      <div className="extent-map map-canvas" ref={container} aria-label="Map of the table's spatial extent">
        {empty && <p className="extent-empty">No geometries in the table</p>}
      </div>
      <figcaption title="Basemap © OpenFreeMap, © OpenMapTiles, data from OpenStreetMap contributors">
        <span>Spatial Extent</span>
        {bbox && (
          <span className="extent-bbox" title="West, south, east, north">
            <code>{formatBBox(bbox)}</code>
            <span className="extent-crs" title="WGS 84 longitude/latitude, the frame the table declares its geometries in">
              CRS84
            </span>
          </span>
        )}
      </figcaption>
    </figure>
  )
}

/** The four corners to at most three decimals, trailing zeros dropped, so the caption stays one line. */
function formatBBox(bbox: BBox) {
  return bbox.map((value) => String(Number(value.toFixed(3)))).join(', ')
}
