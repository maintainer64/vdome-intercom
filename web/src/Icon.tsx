import type { CSSProperties } from 'react'

export type IconName = 'door' | 'camera' | 'activity' | 'settings' | 'arrow' | 'lock' | 'logout' | 'check' | 'shield' | 'bell' | 'refresh' | 'play' | 'stop' | 'signal' | 'key'
const paths: Record<IconName, React.ReactNode> = {
  door: <><path d="M4 21h16M6 21V4l12-2v19M6 4h9"/><path d="M14 12v2"/></>,
  camera: <><path d="m15 10 5-3v10l-5-3"/><rect x="3" y="6" width="12" height="12" rx="3"/></>,
  activity: <><path d="M8 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-3M8 8h5M8 12h3M8 16h7"/><circle cx="19" cy="5" r="3"/></>,
  settings: <><circle cx="12" cy="8" r="4"/><path d="M4 21v-2a6 6 0 0 1 6-6h4a6 6 0 0 1 6 6v2"/></>,
  arrow: <><path d="M5 12h14m-6-6 6 6-6 6"/></>,
  lock: <><rect x="5" y="10" width="14" height="11" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3m-4 5v2"/></>,
  logout: <><path d="M9 4H5v16h4m6-13 5 5-5 5m-6-5h11"/></>,
  check: <path d="m5 12 4 4L19 6"/>,
  shield: <><path d="m12 3 8 3v6c0 4-8 9-8 9s-8-5-8-9V6z"/><path d="m8 12 3 3 5-6"/></>,
  bell: <><path d="M5 17h14l-2-3V9A5 5 0 0 0 7 9v5zm5 3h4"/></>,
  refresh: <><path d="M20 7v5h-5M4 17v-5h5"/><path d="M6 7a7 7 0 0 1 12-1l2 3M4 15l2 3a7 7 0 0 0 12-1"/></>,
  play: <path d="m8 4 12 8-12 8z"/>,
  stop: <rect x="6" y="6" width="12" height="12" rx="2"/>,
  signal: <><path d="M5 20v-3m5 3v-7m5 7V9m5 11V4"/></>,
  key: <><circle cx="8" cy="8" r="5"/><path d="m12 12 9 9m-5-5 3-3m-1 5 3-3"/></>,
}
export function Icon({ name, className = '', style }: { name: IconName; className?: string; style?: CSSProperties }) {
  return <svg className={`icon ${className}`} style={style} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{paths[name]}</svg>
}
