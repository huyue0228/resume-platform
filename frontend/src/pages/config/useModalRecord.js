import { useState } from 'react'

export function useModalRecord() {
  const [state, setState] = useState({ visible: false, record: null })

  return {
    visible: state.visible,
    record: state.record,
    open: (record = null) => setState({ visible: true, record }),
    close: () => setState({ visible: false, record: null }),
  }
}
