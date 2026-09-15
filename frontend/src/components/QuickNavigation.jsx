import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Button, Empty, Input, Modal } from 'antd'
import { ArrowRightOutlined, SearchOutlined } from '@ant-design/icons'

export default function QuickNavigation({ routes }) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const inputRef = useRef(null)
  const navigate = useNavigate()
  const [activeIndex, setActiveIndex] = useState(0)
  const destinations = routes.flatMap((route) => route.routes || [route])
    .filter((route) => route.name.includes(query.trim()))

  useEffect(() => {
    const onShortcut = (event) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setOpen((value) => !value)
      }
    }
    window.addEventListener('keydown', onShortcut)
    return () => window.removeEventListener('keydown', onShortcut)
  }, [])

  return (
    <>
      <Button className="workspace-search" icon={<SearchOutlined />} onClick={() => setOpen(true)}>
        快速导航 <kbd>⌘ / Ctrl K</kbd>
      </Button>
      <Modal
        title="快速导航"
        open={open}
        onCancel={() => setOpen(false)}
        footer={null}
        width={520}
        afterOpenChange={(visible) => {
          if (visible) inputRef.current?.focus()
          else { setQuery(''); setActiveIndex(0) }
        }}
      >
        <Input
          ref={inputRef}
          aria-label="搜索可访问页面"
          prefix={<SearchOutlined />}
          placeholder="搜索页面，例如：简历、岗位、任务"
          value={query}
          allowClear
          onChange={(event) => { setQuery(event.target.value); setActiveIndex(0) }}
          onKeyDown={(event) => {
            if (['ArrowDown', 'ArrowUp'].includes(event.key)) {
              event.preventDefault()
              setActiveIndex((index) => destinations.length ? (index + (event.key === 'ArrowDown' ? 1 : -1) + destinations.length) % destinations.length : 0)
            }
            if (event.key === 'Enter' && destinations.length) { navigate(destinations[Math.min(activeIndex, destinations.length - 1)].path); setOpen(false) }
          }}
          size="large"
        />
        <nav className="workspace-destinations" aria-label="可访问页面">
          {destinations.map((route, index) => (
            <Link key={route.path} to={route.path} className={index === Math.min(activeIndex, destinations.length - 1) ? 'is-keyboard-active' : undefined} onClick={() => setOpen(false)}>
              <span>{route.icon}{route.name}</span><ArrowRightOutlined />
            </Link>
          ))}
          {!destinations.length && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="未找到可访问的页面" />}
        </nav>
      </Modal>
    </>
  )
}
