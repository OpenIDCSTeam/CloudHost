import type { ReactNode } from 'react'
import { IconClose } from './Icons'

type Props = {
  open: boolean
  title: string
  children: ReactNode
  footer?: ReactNode
  onClose: () => void
}

export default function Modal({ open, title, children, footer, onClose }: Props) {
  if (!open) return null
  return (
    <div className="overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <div className="modal__head">
          <h3>{title}</h3>
          <button className="icon-btn" style={{ marginLeft: 'auto' }} onClick={onClose} aria-label="关闭">
            <IconClose size={16} />
          </button>
        </div>
        <div className="modal__body">{children}</div>
        {footer && <div className="modal__foot">{footer}</div>}
      </div>
    </div>
  )
}
