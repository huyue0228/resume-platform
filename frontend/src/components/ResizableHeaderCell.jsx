export default function ResizableHeaderCell({
  width,
  minWidth = 72,
  onResize,
  children,
  ...restProps
}) {
  const handlePointerDown = (event) => {
    if (!width || !onResize) return
    event.preventDefault()
    event.stopPropagation()
    const startX = event.clientX
    const startWidth = width

    const handleMove = (moveEvent) => {
      const nextWidth = Math.max(minWidth, startWidth + moveEvent.clientX - startX)
      onResize(nextWidth)
    }
    const handleUp = () => {
      document.removeEventListener('pointermove', handleMove)
      document.removeEventListener('pointerup', handleUp)
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }

    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
    document.addEventListener('pointermove', handleMove)
    document.addEventListener('pointerup', handleUp)
  }

  return (
    <th
      {...restProps}
      style={{
        ...restProps.style,
        width,
        position: 'relative',
      }}
    >
      {children}
      {onResize && width ? (
        <span
          className="srf-column-resize-handle"
          onPointerDown={handlePointerDown}
        />
      ) : null}
    </th>
  )
}
