import { useEffect, useRef, useState } from 'react'
import maplibregl, { type GeoJSONSource } from 'maplibre-gl'
import 'maplibre-gl/dist/maplibre-gl.css'
import { fetchExtent, type BBox } from '../api'
import { addExtentLayers, BASEMAP, EXTENT_SOURCE } from '../basemap'
import { mapPalette, useTheme } from '../theme'

type ExtentMapProps = {
  /** Changes whenever the stats are reloaded, so the extent is read again with them. */
  refreshKey: unknown
}

/**
 * The small map on the Stats card: the table's spatial extent as a dotted
 * outline over a basemap, zoomable and nothing more. The Map tab is where
 * geometries are queried; this only says where the data is.
 */
export function ExtentMap({ refreshKey }: ExtentMapProps) {
  const container = useRef<HTMLDivElement>(null)
  const mapRef = useRef<maplibregl.Map | null>(null)
  const [theme] = useTheme()
  const appliedTheme = useRef(theme)
  // 0 until the first style has loaded, so the extent is only drawn into a map
  // that has the layers to draw it with.
  const [styleVersion, setStyleVersion] = useState(0)
  const [bbox, setBBox] = useState<BBox | null>(null)
  const [empty, setEmpty] = useState(false)

  useEffect(() => {
    const parent = container.current
    if (!parent) return
    const map = new maplibregl.Map({
      container: parent,
      style: BASEMAP[appliedTheme.current],
      center: [0, 20],
      zoom: 0.6,
      // One world: an extent spanning the globe would otherwise be drawn on
      // every copy, and a map this small shows two of them.
      renderWorldCopies: false,
      // No chrome: the zoom buttons and the attribution control would take a
      // third of a map this size. It still zooms by wheel and double click,
      // and the basemap credit is on the caption; the Map tab shows it in full.
      attributionControl: false,
    })
    map.on('error', (event) => {
      if (!('tile' in event)) console.error(event.error)
    })
    map.on('style.load', () => {
      addExtentLayers(map, mapPalette(appliedTheme.current).extent)
      setStyleVersion((version) => version + 1)
    })
    mapRef.current = map
    return () => {
      mapRef.current = null
      setStyleVersion(0)
      map.remove()
    }
  }, [])

  // The basemap follows the theme; the layers come back through 'style.load'.
  useEffect(() => {
    const map = mapRef.current
    if (!map || appliedTheme.current === theme) return
    appliedTheme.current = theme
    map.setStyle(BASEMAP[theme])
  }, [theme])

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
  }, [styleVersion, bbox])

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
