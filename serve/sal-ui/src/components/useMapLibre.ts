import { useEffect, useRef, useState, type RefObject } from 'react'
import * as maplibregl from 'maplibre-gl'
import type { LngLat, MapGeoJSONFeature, MapOptions } from 'maplibre-gl'
import 'maplibre-gl/dist/maplibre-gl.css'
import workerUrl from 'maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url'
import { BASEMAP } from '../basemap'
import { mapPalette, useTheme, type MapPalette } from '../theme'

// MapLibre 6 is ES modules only and loads its worker by URL, which a bundler
// has to be told: Vite emits the worker as an asset of its own and this is
// its path under the server root.
maplibregl.setWorkerUrl(workerUrl)

export type MapLibreOptions = {
  /**
   * Adds the sources and layers the caller draws into, into a style that has
   * just loaded. A theme switch loads the other basemap, which drops every
   * layer, so this runs again with the other palette; the caller refills the
   * sources when styleVersion changes.
   */
  addLayers: (map: maplibregl.Map, palette: MapPalette) => void
  /** The layers a click is looked up in, topmost first; the cursor is a pointer over them. */
  featureLayers?: string[]
  /** A click on one or more features of featureLayers. */
  onFeatureClick?: (map: maplibregl.Map, hits: MapGeoJSONFeature[], point: LngLat) => void
  /** A click away from any feature. */
  onEmptyClick?: (map: maplibregl.Map, point: LngLat) => void
  /** The zoom and scale controls, on unless the map is too small for them. */
  controls?: boolean
  /** A control that takes the map full screen, for a map that is the view. */
  fullscreen?: boolean
  /** Overrides for the map's construction, such as the starting zoom. */
  mapOptions?: Partial<Omit<MapOptions, 'container' | 'style'>>
  /**
   * Whether the container is in the page. A view that renders its map only
   * once there is something to draw passes false until then; the map is
   * built when it turns true and torn down when it turns false again.
   */
  enabled?: boolean
}

export type MapLibreHandle = {
  map: RefObject<maplibregl.Map | null>
  /**
   * Counts the basemap styles that have loaded: 0 until the first one has,
   * and one more for each theme switch. An effect that fills a source waits
   * for it to be non-zero and runs again when it changes.
   */
  styleVersion: number
  /** Opens a popup at a point with the given content, replacing the one before it. */
  showPopup: (point: LngLat, content: HTMLElement) => void
}

/**
 * A MapLibre map in the container, on the basemap the theme names and
 * following the theme when it changes, with the clicks and the popup every
 * map in the UI handles the same way. The map is built once; the callbacks
 * are read through a ref so the latest ones run without rebuilding it.
 */
export function useMapLibre(container: RefObject<HTMLDivElement | null>, options: MapLibreOptions): MapLibreHandle {
  const mapRef = useRef<maplibregl.Map | null>(null)
  const popupRef = useRef<maplibregl.Popup | null>(null)
  const [styleVersion, setStyleVersion] = useState(0)
  const [theme] = useTheme()
  const appliedTheme = useRef(theme)
  const current = useRef(options)
  current.current = options

  const enabled = options.enabled ?? true

  useEffect(() => {
    const parent = container.current
    if (!parent || !enabled) return
    const { controls = true, fullscreen = false, mapOptions = {}, featureLayers = [] } = current.current
    const map = new maplibregl.Map({
      container: parent,
      style: BASEMAP[appliedTheme.current],
      center: [0, 20],
      zoom: 1,
      attributionControl: { compact: true },
      ...mapOptions,
    })
    if (fullscreen) map.addControl(new maplibregl.FullscreenControl(), 'top-right')
    if (controls) {
      map.addControl(new maplibregl.NavigationControl({ showCompass: false }), 'top-right')
      map.addControl(new maplibregl.ScaleControl(), 'bottom-left')
    }
    // A basemap tile that cannot be fetched is not worth a console error per
    // tile; the features draw without it.
    map.on('error', (event) => {
      if (!('tile' in event)) console.error(event.error)
    })
    // Fires for the first style and again for each one setStyle loads.
    map.on('style.load', () => {
      current.current.addLayers(map, mapPalette(appliedTheme.current))
      setStyleVersion((version) => version + 1)
    })
    map.on('click', (event) => {
      popupRef.current?.remove()
      popupRef.current = null
      // The layers exist once a style has loaded; between a theme switch and
      // the new style's load there is nothing to hit.
      const present = featureLayers.filter((layer) => map.getLayer(layer))
      const hits = present.length > 0 ? map.queryRenderedFeatures(event.point, { layers: present }) : []
      if (hits.length === 0) {
        current.current.onEmptyClick?.(map, event.lngLat)
        return
      }
      current.current.onFeatureClick?.(map, hits, event.lngLat)
    })
    for (const layer of featureLayers) {
      map.on('mouseenter', layer, () => {
        map.getCanvas().style.cursor = 'pointer'
      })
      map.on('mouseleave', layer, () => {
        map.getCanvas().style.cursor = ''
      })
    }
    mapRef.current = map
    return () => {
      popupRef.current?.remove()
      popupRef.current = null
      mapRef.current = null
      setStyleVersion(0)
      map.remove()
    }
  }, [container, enabled])

  // The basemap follows the theme; the layers come back through 'style.load'.
  useEffect(() => {
    const map = mapRef.current
    if (!map || appliedTheme.current === theme) return
    appliedTheme.current = theme
    map.setStyle(BASEMAP[theme])
  }, [theme])

  const showPopup = (point: LngLat, content: HTMLElement) => {
    const map = mapRef.current
    if (!map) return
    popupRef.current?.remove()
    popupRef.current = new maplibregl.Popup({ maxWidth: '420px', className: 'map-popup' })
      .setLngLat(point)
      .setDOMContent(content)
      .addTo(map)
  }

  return { map: mapRef, styleVersion, showPopup }
}

/** Renders a feature's properties as a table for its popup, built as DOM so a value is never parsed as HTML. */
export function propertiesTable(properties: Record<string, unknown>): HTMLTableElement {
  const table = document.createElement('table')
  table.className = 'map-popup-table'
  for (const [key, value] of Object.entries(properties)) {
    const row = table.insertRow()
    const name = document.createElement('th')
    name.textContent = key
    row.appendChild(name)
    const cell = row.insertCell()
    const text = value === null || value === undefined ? '' : typeof value === 'string' ? value : JSON.stringify(value)
    if (/^https?:\/\//.test(text)) {
      const link = document.createElement('a')
      link.href = text
      link.target = '_blank'
      link.rel = 'noreferrer'
      link.textContent = text
      cell.appendChild(link)
    } else {
      cell.textContent = text
    }
  }
  return table
}
