import { Fragment, type ReactNode } from 'react'

/*
 * The little markdown the assistant's prose needs, rendered without a
 * markdown dependency: fenced code blocks, which a SPARQL query comes in and
 * which get the caller's buttons, bullet lists, paragraphs, and inline code
 * and bold. Anything else is shown as the text it is. Text still streaming
 * may end inside a fence, which is rendered as the block so far.
 */

type Block = { kind: 'code'; language: string; code: string } | { kind: 'list'; items: string[] } | { kind: 'paragraph'; text: string }

function parseBlocks(text: string): Block[] {
  const blocks: Block[] = []
  const lines = text.split('\n')
  let i = 0
  while (i < lines.length) {
    const fence = /^\s*```\s*(\w*)\s*$/.exec(lines[i])
    if (fence) {
      const code: string[] = []
      i++
      while (i < lines.length && !/^\s*```\s*$/.test(lines[i])) code.push(lines[i++])
      i++
      blocks.push({ kind: 'code', language: fence[1].toLowerCase(), code: code.join('\n') })
      continue
    }
    if (/^\s*[-*]\s+/.test(lines[i])) {
      const items: string[] = []
      while (i < lines.length && /^\s*[-*]\s+/.test(lines[i])) items.push(lines[i++].replace(/^\s*[-*]\s+/, ''))
      blocks.push({ kind: 'list', items })
      continue
    }
    if (lines[i].trim() === '') {
      i++
      continue
    }
    const paragraph: string[] = []
    while (i < lines.length && lines[i].trim() !== '' && !/^\s*```/.test(lines[i]) && !/^\s*[-*]\s+/.test(lines[i])) {
      paragraph.push(lines[i++].replace(/^#+\s*/, ''))
    }
    blocks.push({ kind: 'paragraph', text: paragraph.join(' ') })
  }
  return blocks
}

/** Inline code and bold, the two spans the assistant uses. */
function inline(text: string): ReactNode[] {
  const nodes: ReactNode[] = []
  const pattern = /(`[^`]+`|\*\*[^*]+\*\*)/g
  let last = 0
  let key = 0
  for (const match of text.matchAll(pattern)) {
    if (match.index > last) nodes.push(text.slice(last, match.index))
    const token = match[0]
    if (token.startsWith('`')) nodes.push(<code key={key++}>{token.slice(1, -1)}</code>)
    else nodes.push(<strong key={key++}>{token.slice(2, -2)}</strong>)
    last = match.index + token.length
  }
  if (last < text.length) nodes.push(text.slice(last))
  return nodes
}

type MarkdownProps = {
  text: string
  /** Renders a fenced code block; the default is a plain <pre>. */
  renderCode?: (code: string, language: string) => ReactNode
}

export function Markdown({ text, renderCode }: MarkdownProps) {
  return (
    <>
      {parseBlocks(text).map((block, i) => {
        if (block.kind === 'code') {
          return <Fragment key={i}>{renderCode ? renderCode(block.code, block.language) : <pre>{block.code}</pre>}</Fragment>
        }
        if (block.kind === 'list') {
          return (
            <ul key={i}>
              {block.items.map((item, j) => (
                <li key={j}>{inline(item)}</li>
              ))}
            </ul>
          )
        }
        return <p key={i}>{inline(block.text)}</p>
      })}
    </>
  )
}
