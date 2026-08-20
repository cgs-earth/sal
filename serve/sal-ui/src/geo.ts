import { wktToGeoJSON } from 'betterknown'
import type { Feature, Geometry, Position } from 'geojson'
import type { BBox } from './api'

export type MapFeature = Feature<Geometry, Record<string, string>>

/**
 * The geometry types a cell can hold, as WKT (what the object
 * projection and ST_AsText render) or GeoJSON (what ST_AsGeoJSON renders). The
 * WKT check is a cheap prefix test so that every cell of a result can be tried;
 * an EWKT SRID or a GeoSPARQL CRS IRI may lead the string.
 */
const WKT_PREFIX = /^(SRID=\d+;)?\s*(<[^>]*>\s*)?(POINT|LINESTRING|POLYGON|MULTIPOINT|MULTILINESTRING|MULTIPOLYGON|GEOMETRYCOLLECTION)\b/i
const GEOJSON_TYPES = new Set([
  'Point',
  'LineString',
  'Polygon',
  'MultiPoint',
  'MultiLineString',
  'MultiPolygon',
  'GeometryCollection',
])

/** Parses a cell as WKT or GeoJSON, or returns null when it is neither. */
export function parseGeometry(value: string): Geometry | null {
  const trimmed = value.trim()
  if (trimmed.startsWith('{')) {
    try {
      const parsed = JSON.parse(trimmed) as { type?: unknown }
      return typeof parsed.type === 'string' && GEOJSON_TYPES.has(parsed.type) ? (parsed as Geometry) : null
    } catch {
      return null
    }
  }
  if (!WKT_PREFIX.test(trimmed)) return null
  try {
    // The stored geometries carry no CRS, so a leading CRS IRI is dropped rather than honored.
    return wktToGeoJSON(trimmed.replace(/^<[^>]*>\s*/, '')) ?? null
  } catch {
    return null
  }
}

export type ResultFeatures = {
  features: MapFeature[]
  /** The columns at least one geometry was read from. */
  geometryColumns: string[]
  /** Rows that held no geometry in any column. */
  rowsWithoutGeometry: number
}

/**
 * Turns a tabular result into features: one per geometry-valued cell, carrying
 * every other column of its row as properties so a feature on the map can show
 * what the query said about it. A row with geometry in two columns yields two
 * features, each naming the column it came from.
 */
export function featuresFromResult(header: string[], rows: string[][]): ResultFeatures {
  const features: MapFeature[] = []
  const geometryColumns = new Set<string>()
  let rowsWithoutGeometry = 0
  rows.forEach((row, rowIndex) => {
    const geometries: { column: number; geometry: Geometry }[] = []
    row.forEach((value, column) => {
      const geometry = parseGeometry(value)
      if (geometry) geometries.push({ column, geometry })
    })
    if (geometries.length === 0) {
      rowsWithoutGeometry++
      return
    }
    for (const { column, geometry } of geometries) {
      geometryColumns.add(header[column] ?? `column ${column + 1}`)
      const properties: Record<string, string> = { '#': String(rowIndex + 1) }
      if (geometries.length > 1) properties.geometry = header[column] ?? `column ${column + 1}`
      header.forEach((name, index) => {
        if (index !== column && !geometries.some((g) => g.column === index)) properties[name] = row[index] ?? ''
      })
      features.push({ type: 'Feature', geometry, properties })
    }
  })
  return { features, geometryColumns: [...geometryColumns], rowsWithoutGeometry }
}

/** The bounding box of a set of features, or null when none has a position. */
export function boundsOf(features: Feature[]): BBox | null {
  let bounds: BBox | null = null
  const extend = ([x, y]: Position) => {
    if (!Number.isFinite(x) || !Number.isFinite(y)) return
    bounds = bounds
      ? [Math.min(bounds[0], x), Math.min(bounds[1], y), Math.max(bounds[2], x), Math.max(bounds[3], y)]
      : [x, y, x, y]
  }
  const walk = (geometry: Geometry) => {
    switch (geometry.type) {
      case 'Point':
        extend(geometry.coordinates)
        break
      case 'MultiPoint':
      case 'LineString':
        geometry.coordinates.forEach(extend)
        break
      case 'MultiLineString':
      case 'Polygon':
        geometry.coordinates.forEach((ring) => ring.forEach(extend))
        break
      case 'MultiPolygon':
        geometry.coordinates.forEach((polygon) => polygon.forEach((ring) => ring.forEach(extend)))
        break
      case 'GeometryCollection':
        geometry.geometries.forEach(walk)
        break
    }
  }
  for (const feature of features) if (feature.geometry) walk(feature.geometry)
  return bounds
}

/** A box of `size` degrees on each side centered on a point, kept inside the valid latitude range. */
export function boxAround(lng: number, lat: number, size: number): BBox {
  const half = size / 2
  return [lng - half, Math.max(-90, lat - half), lng + half, Math.min(90, lat + half)]
}

/** A polygon feature outlining a box, for drawing it on the map. */
export function boxPolygon([w, s, e, n]: BBox): Feature {
  return {
    type: 'Feature',
    properties: {},
    geometry: {
      type: 'Polygon',
      coordinates: [
        [
          [w, s],
          [e, s],
          [e, n],
          [w, n],
          [w, s],
        ],
      ],
    },
  }
}
