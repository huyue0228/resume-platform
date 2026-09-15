import { Children, cloneElement, isValidElement, useRef, useState } from 'react'
import { MoreOutlined } from '@ant-design/icons'
import { Button, Popover } from 'antd'

function accessibleActions(children) {
  return Children.map(children, (child) => {
    if (!isValidElement(child)) return child
    const props = child.type === 'a' && !child.props.href ? { role: 'button', tabIndex: 0 } : {}
    if (child.props.children && typeof child.props.children !== 'function') props.children = accessibleActions(child.props.children)
    return cloneElement(child, props)
  })
}

export default function TableRowActions({ children }) {
  const [open, setOpen] = useState(false)
  const contentRef = useRef(null)
  const buttonRef = useRef(null)
  if (!children) return null
  return <Popover
    trigger="click"
    placement="bottomRight"
    open={open}
    onOpenChange={setOpen}
    afterOpenChange={(visible) => {
      if (!visible) return
      contentRef.current?.querySelector('a,button')?.focus()
    }}
    content={<div ref={contentRef} className="srf-row-actions-menu" role="group" aria-label="记录操作" onKeyDown={(event) => {
      if (event.key === 'Escape') { setOpen(false); buttonRef.current?.focus(); event.stopPropagation() }
      if (event.target.matches('a:not([href])') && ['Enter', ' '].includes(event.key)) { event.preventDefault(); event.target.click() }
      if (['ArrowDown', 'ArrowUp'].includes(event.key)) {
        event.preventDefault()
        const items = [...event.currentTarget.querySelectorAll('a,button:not(:disabled)')]
        const next = (items.indexOf(document.activeElement) + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length
        items[next]?.focus()
      }
    }}>{accessibleActions(children)}</div>}
  >
    <Button ref={buttonRef} type="text" className="srf-row-actions-trigger" aria-label="更多操作" title="更多操作" aria-expanded={open} icon={<MoreOutlined />} />
  </Popover>
}
