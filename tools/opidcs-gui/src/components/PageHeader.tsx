import type { ReactNode } from 'react'

type Props = {
  title: string
  desc?: string
  actions?: ReactNode
}

export default function PageHeader({ title, desc, actions }: Props) {
  return (
    <div className="page-head">
      <div>
        <h1>{title}</h1>
        {desc && <p>{desc}</p>}
      </div>
      {actions && <div className="page-head__actions">{actions}</div>}
    </div>
  )
}
