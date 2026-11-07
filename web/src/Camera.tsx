import { useEffect, useRef, useState } from 'react'
import type HlsType from 'hls.js'
import { api, type Device } from './api'
import { Icon } from './Icon'

type Stream = { id: string; url: string }

function LiveVideo({ stream }: { stream: Stream }) {
  const video = useRef<HTMLVideoElement>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    let cancelled = false
    let hls: HlsType | undefined
    const player = video.current!
    async function attach() {
      const { default: Hls } = await import('hls.js')
      if (cancelled) return
      if (!Hls.isSupported()) {
        if (player.canPlayType('application/vnd.apple.mpegurl')) { player.src = stream.url; void player.play().catch(() => {}); return }
        setError('Браузер не поддерживает воспроизведение HLS'); return
      }
      hls = new Hls({ enableWorker: true, liveSyncDurationCount: 2, backBufferLength: 0, maxBufferLength: 6, maxMaxBufferLength: 12 })
      hls.on(Hls.Events.ERROR, (_event, data) => { if (data.fatal) { hls?.stopLoad(); setError(`Видеопоток прервался (${data.details}). Можно остановить эфир и запросить кадр вручную.`) } })
      hls.on(Hls.Events.MANIFEST_PARSED, () => { void player.play().catch(() => {}) })
      hls.loadSource(stream.url)
      hls.attachMedia(player)
    }
    void attach().catch(() => { if (!cancelled) setError('Не удалось запустить видеоплеер') })
    return () => { cancelled = true; hls?.destroy(); player.pause(); player.removeAttribute('src'); player.load() }
  }, [stream.url])
  return <div className="live-player">
    <div className="live-caption" role="status"><span className="live-pill"><span className="status-dot"/>ПРЯМОЙ ЭФИР</span><span>Без звука</span></div>
    {error && <p role="alert" className="camera-error">{error}</p>}
    <video ref={video} controls autoPlay muted playsInline aria-label="Прямой эфир камеры" onError={() => setError('Не удалось воспроизвести видеопоток')}/>
  </div>
}

export function Camera({ device, active = true }: { device: Device; active?: boolean }) {
  const [stream, setStream] = useState<Stream | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const current = useRef<Stream | null>(null)
  const generation = useRef(0)
  useEffect(() => () => {
    generation.current++
    if (current.current) void api(`/streams/${current.current.id}`, 'DELETE').catch(() => {})
  }, [])
  useEffect(() => {
    if (!active) { generation.current++; stop(); setBusy(false) }
  }, [active])
  async function start() {
    const revision = generation.current
    setBusy(true); setError('')
    try {
      const next = await api<Stream>(`/devices/${encodeURIComponent(device.id)}/stream`, 'POST')
      if (revision !== generation.current) { void api(`/streams/${next.id}`, 'DELETE').catch(() => {}); return }
      current.current = next; setStream(next)
    } catch (e) { if (revision === generation.current) setError(e instanceof Error ? e.message : 'Видеопоток недоступен') }
    finally { if (revision === generation.current) setBusy(false) }
  }
  function stop() {
    if (current.current) void api(`/streams/${current.current.id}`, 'DELETE').catch(() => {})
    current.current = null; setStream(null)
  }
  return <div className="camera-content">
    {stream && <LiveVideo stream={stream}/>}
    <div hidden={!!stream || busy}><Snapshot device={device}/></div>
    {error && <p role="alert" className="camera-error">{error}</p>}
    {device.hasStream && (stream ? <button className="button button-dark stream-button" onClick={stop}><Icon name="stop"/>Остановить эфир</button> : <button className="button button-dark stream-button" disabled={busy || !active} onClick={() => void start()}><Icon name={busy ? 'refresh' : 'play'} className={busy ? 'spin' : ''}/>{busy ? 'Подключаем видеопоток…' : 'Смотреть эфир'}</button>)}
    {busy && <div className="media-frame camera-loading" role="status"><Icon name="camera"/><span>Подключаемся к камере</span><small>Это может занять до 25 секунд</small></div>}
  </div>
}

function Snapshot({ device }: { device: Device }) {
  const [source, setSource] = useState('')
  const [failure, setFailure] = useState('')
  const [retry, setRetry] = useState(0)
  const [loading, setLoading] = useState(false)
  const sourceURL = useRef('')
  useEffect(() => () => { if (sourceURL.current) URL.revokeObjectURL(sourceURL.current) }, [])
  useEffect(() => {
    const controller = new AbortController()
    async function load() {
      setLoading(true)
      try {
        const response = await fetch(`/api/devices/${encodeURIComponent(device.id)}/snapshot`, { credentials: 'same-origin', signal: controller.signal })
        if (!response.ok) throw new Error(`Не удалось получить кадр (HTTP ${response.status})`)
        const blob = await response.blob()
        if (controller.signal.aborted) return
        const old = sourceURL.current
        sourceURL.current = URL.createObjectURL(blob)
        setSource(sourceURL.current); setFailure('')
        if (old) URL.revokeObjectURL(old)
      } catch (e) { if (!controller.signal.aborted) setFailure(e instanceof Error ? e.message : 'Камера недоступна') }
      finally { if (!controller.signal.aborted) setLoading(false) }
    }
    void load()
    return () => { controller.abort() }
  }, [device.id, retry])
  return <figure className="snapshot">
    <div className="media-frame">
      {source ? <img src={source} alt={`Камера: ${device.name}`} onError={() => setFailure('Не удалось показать полученное изображение')}/> : <div className="camera-placeholder"><Icon name="camera"/><span>{loading ? 'Получаем запасной кадр…' : 'Кадр недоступен'}</span><small>{loading ? 'Один запрос, без автообновления' : 'Можно попробовать обновить вручную'}</small></div>}
      {source && <span className="frame-label">ЗАПАСНОЙ КАДР</span>}
    </div>
    <div className="snapshot-footer"><figcaption>Кадр · обновление вручную</figcaption><button className="button button-quiet button-small" disabled={loading} onClick={() => setRetry(n => n + 1)}><Icon name="refresh" className={loading ? 'spin' : ''}/>{loading ? 'Загрузка…' : 'Обновить кадр'}</button></div>
    {failure && <p role="status" className="camera-error">{failure}</p>}
  </figure>
}
