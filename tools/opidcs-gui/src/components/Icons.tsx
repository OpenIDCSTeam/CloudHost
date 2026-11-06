type Props = { size?: number }

const base = (children: React.ReactNode, size = 18) => (
  <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
    {children}
  </svg>
)

export const IconGrid = ({ size }: Props) => base(<><rect x="3" y="3" width="7" height="7" rx="1.5" /><rect x="14" y="3" width="7" height="7" rx="1.5" /><rect x="3" y="14" width="7" height="7" rx="1.5" /><rect x="14" y="14" width="7" height="7" rx="1.5" /></>, size)

export const IconServer = ({ size }: Props) => base(<><rect x="3" y="4" width="18" height="7" rx="2" /><rect x="3" y="13" width="18" height="7" rx="2" /><path d="M7 7.5h.01M7 16.5h.01" /></>, size)

export const IconBox = ({ size }: Props) => base(<><path d="m12 3 8 4.5v9L12 21l-8-4.5v-9L12 3Z" /><path d="m4 7.5 8 4.5 8-4.5M12 12v9" /></>, size)

export const IconNetwork = ({ size }: Props) => base(<><rect x="3" y="3" width="6" height="6" rx="1.5" /><rect x="15" y="15" width="6" height="6" rx="1.5" /><rect x="15" y="3" width="6" height="6" rx="1.5" /><path d="M9 6h6M6 9v6a3 3 0 0 0 3 3h6" /></>, size)

export const IconGlobe = ({ size }: Props) => base(<><circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3c2.5 2.5 3.8 5.6 3.8 9s-1.3 6.5-3.8 9c-2.5-2.5-3.8-5.6-3.8-9S9.5 5.5 12 3Z" /></>, size)

export const IconUsers = ({ size }: Props) => base(<><circle cx="9" cy="8" r="3.2" /><path d="M2.8 20a6.2 6.2 0 0 1 12.4 0M16 5.2a3 3 0 0 1 0 5.6M17.5 14.4A5.5 5.5 0 0 1 21.2 20" /></>, size)

export const IconTasks = ({ size }: Props) => base(<><path d="M9 5h11M9 12h11M9 19h11" /><path d="m3.5 5 1.4 1.4L7 4M3.5 12l1.4 1.4L7 11M3.5 19l1.4 1.4L7 18" /></>, size)

export const IconLogs = ({ size }: Props) => base(<><path d="M5 3h9l5 5v13H5z" /><path d="M14 3v5h5M8 12h8M8 16h6" /></>, size)

export const IconSettings = ({ size }: Props) => base(<><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.87l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.7 1.7 0 0 0-1.87-.34 1.7 1.7 0 0 0-1 1.56V21a2 2 0 1 1-4 0v-.09a1.7 1.7 0 0 0-1.1-1.56 1.7 1.7 0 0 0-1.87.34l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-1.56-1H3a2 2 0 1 1 0-4h.09A1.7 1.7 0 0 0 4.6 8.9a1.7 1.7 0 0 0-.34-1.87l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.7 1.7 0 0 0 8.9 4.6h.1a1.7 1.7 0 0 0 1-1.56V3a2 2 0 1 1 4 0v.09a1.7 1.7 0 0 0 1 1.56 1.7 1.7 0 0 0 1.87-.34l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.7 1.7 0 0 0-.34 1.87v.1a1.7 1.7 0 0 0 1.56 1H21a2 2 0 1 1 0 4h-.09a1.7 1.7 0 0 0-1.51 1Z" /></>, size)

export const IconPlus = ({ size }: Props) => base(<path d="M12 5v14M5 12h14" />, size)

export const IconRefresh = ({ size }: Props) => base(<><path d="M21 12a9 9 0 1 1-2.6-6.4" /><path d="M21 3v6h-6" /></>, size)

export const IconClose = ({ size }: Props) => base(<path d="m6 6 12 12M18 6 6 18" />, size)

export const IconSearch = ({ size }: Props) => base(<><circle cx="11" cy="11" r="7" /><path d="m20 20-3.5-3.5" /></>, size)

export const IconPower = ({ size }: Props) => base(<><path d="M12 3v9" /><path d="M6.4 6.4a8 8 0 1 0 11.2 0" /></>, size)

export const IconTrash = ({ size }: Props) => base(<><path d="M4 7h16M9 7V4h6v3M6 7l1 14h10l1-14" /></>, size)

export const IconPlay = ({ size }: Props) => base(<path d="M7 4.5v15l13-7.5-13-7.5Z" />, size)

export const IconStop = ({ size }: Props) => base(<rect x="6" y="6" width="12" height="12" rx="2" />, size)

export const IconSun = ({ size }: Props) => base(<><circle cx="12" cy="12" r="4" /><path d="M12 2v2M12 20v2M2 12h2M20 12h2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M19.1 4.9l-1.4 1.4M6.3 17.7l-1.4 1.4" /></>, size)

export const IconMoon = ({ size }: Props) => base(<path d="M20 14.5A8.5 8.5 0 0 1 9.5 4a7.5 7.5 0 1 0 10.5 10.5Z" />, size)
