import type { NodeHoverDrawingFunction } from 'sigma/rendering'
import { currentTheme, graphPalette } from '../../theme'

/**
 * Sigma's built-in hover renderer (drawDiscNodeHover) hardcodes a white label-pill
 * background (`context.fillStyle = "#FFF"`) with no way to theme it via settings or
 * CSS — it's a canvas draw call, not styled DOM. This is a copy of its geometry in
 * the page theme's colors instead, read on each draw so a theme switch takes
 * effect on the next hover, so hovering a node doesn't flash white on a dark canvas.
 */
export const drawNodeHover: NodeHoverDrawingFunction = (context, data, settings) => {
  const size = settings.labelSize
  const font = settings.labelFont
  const weight = settings.labelWeight
  context.font = `${weight} ${size}px ${font}`

  const palette = graphPalette(currentTheme())
  context.fillStyle = palette.hoverBackground
  context.shadowOffsetX = 0
  context.shadowOffsetY = 0
  context.shadowBlur = 8
  context.shadowColor = palette.hoverShadow

  const PADDING = 2
  if (typeof data.label === 'string') {
    const textWidth = context.measureText(data.label).width
    const boxWidth = Math.round(textWidth + 5)
    const boxHeight = Math.round(size + 2 * PADDING)
    const radius = Math.max(data.size, size / 2) + PADDING
    const angleRadian = Math.asin(boxHeight / 2 / radius)
    const xDeltaCoord = Math.sqrt(Math.abs(radius ** 2 - (boxHeight / 2) ** 2))
    context.beginPath()
    context.moveTo(data.x + xDeltaCoord, data.y + boxHeight / 2)
    context.lineTo(data.x + radius + boxWidth, data.y + boxHeight / 2)
    context.lineTo(data.x + radius + boxWidth, data.y - boxHeight / 2)
    context.lineTo(data.x + xDeltaCoord, data.y - boxHeight / 2)
    context.arc(data.x, data.y, radius, angleRadian, -angleRadian)
    context.closePath()
    context.fill()
  } else {
    context.beginPath()
    context.arc(data.x, data.y, data.size + PADDING, 0, Math.PI * 2)
    context.closePath()
    context.fill()
  }

  context.shadowOffsetX = 0
  context.shadowOffsetY = 0
  context.shadowBlur = 0

  if (data.label) {
    context.fillStyle = palette.label
    context.fillText(data.label, data.x + data.size + 3, data.y + size / 3)
  }
}
