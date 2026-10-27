import type maplibregl from 'maplibre-gl'
import type { Theme } from './theme'

/** A dark and a light basemap, so a map follows the page theme; both are neutral grays rather than tinted. */
export const BASEMAP: Record<Theme, string> = {
  dark: 'https://tiles.openfreemap.org/styles/dark',
  light: 'https://tiles.openfreemap.org/styles/positron',
}

/** The source name the dataset extent is drawn from, shared by the Map tab and the Stats card. */
export const EXTENT_SOURCE = 'extent'

/**
 * Adds the source and the two layers that draw the dataset extent, a faint fill
 * under a dotted outline, into a map whose style has loaded. MapLibre paint
 * wants a literal color, so the theme's palette color is passed in; a theme
 * switch loads the other basemap, which drops these, and the caller adds them
 * again in the other palette.
 */
export function addExtentLayers(map: maplibregl.Map, color: string) {
  map.addSource(EXTENT_SOURCE, {
    type: 'geojson',
    data: { type: 'FeatureCollection', features: [] },
  })
  map.addLayer({
    id: 'extent-fill',
    type: 'fill',
    source: EXTENT_SOURCE,
    paint: { 'fill-color': color, 'fill-opacity': 0.08 },
  })
  map.addLayer({
    id: 'extent-line',
    type: 'line',
    source: EXTENT_SOURCE,
    paint: { 'line-color': color, 'line-width': 1.5, 'line-dasharray': [4, 3] },
  })
}
