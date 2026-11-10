import { StrictMode, useEffect, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'
import { createRoot } from 'react-dom/client'
import { api, type Device, type Mode, type State } from './api'
import { Camera } from './Camera'
import { Icon, type IconName } from './Icon'
import './style.css'

type Tab = 'doors' | 'cameras' | 'events' | 'account'
const tabs: { id: Tab; title: string; icon: IconName; heading: string; subtitle: string }[] = [
  { id: 'doors', title: 'Домофоны', icon: 'door', heading: 'Ваши домофоны', subtitle: 'Открывайте дверь и управляйте доступом в подъезд.' },
  { id: 'cameras', title: 'Камеры', icon: 'camera', heading: 'Посмотреть, кто у двери', subtitle: 'Прямой эфир с камер вашего дома и запасной кадр.' },
  { id: 'events', title: 'Журнал', icon: 'activity', heading: 'Вызовы и открытия', subtitle: 'Входящие вызовы, команды с кнопки и автооткрытие.' },
  { id: 'account', title: 'Аккаунт', icon: 'settings', heading: 'Ваше подключение', subtitle: 'Сессия, состояние связи и работа автооткрытия.' },
]
const labels: Record<Mode, string> = { off: 'Выключено', next: 'Один вызов', always: 'Каждый вызов' }
const statuses: Record<string, string> = { online: 'В сети', offline: 'Нет связи', unknown: 'Нет данных' }
const results: Record<string, string> = { received: 'Вызов получен', accepted: 'Команда принята', unconfirmed: 'Не подтверждено', cooldown: 'Пропущено: пауза 10 сек.' }
const callStates: Record<string, string> = { waiting: 'Ожидаем входа и загрузки устройств', connecting: 'Соединяемся с сервером вызовов', registering: 'Регистрируем приём вызовов', registered: 'Готовы принимать вызовы', retrying: 'Переподключаемся', error: 'Не удалось подключить вызовы', closed: 'Соединение закрыто' }
const callReasons: Record<string, string> = { credentials_missing: 'VDome не вернул данные для приёма вызовов.', invalid_endpoint: 'Некорректный адрес сервера вызовов.', websocket_failed: 'Сервер вызовов не принял WebSocket-соединение.', subprotocol_mismatch: 'Сервер не подтвердил протокол SIP.', registration_timeout: 'Сервер не ответил на регистрацию вызовов.', authentication_failed: 'Сервер отклонил данные SIP.', unsupported_authentication: 'Сервер запросил неподдерживаемую авторизацию SIP.', registration_rejected: 'Сервер отклонил регистрацию вызовов.', invalid_expiry: 'Сервер не выдал действующую регистрацию.', invalid_message: 'Сервер прислал некорректное SIP-сообщение.', connection_closed: 'Соединение с сервером вызовов прервано.', read_timeout: 'Сервер вызовов перестал отвечать на поддержание соединения.' }
const inviteRejections: Record<string, string> = { not_registered: 'Вызов пришёл до завершения SIP-регистрации.', invalid_call_id: 'Идентификатор вызова отсутствует или превышает допустимую длину.', recipient_mismatch: 'Адрес доставки вызова не соответствует SIP-регистрации аккаунта.', recipient_user_mismatch: 'Абонент в адресе доставки не совпадает с SIP-регистрацией аккаунта.', recipient_host_mismatch: 'Домен в адресе доставки не совпадает с SIP-регистрацией аккаунта.', invalid_request_uri: 'Адрес доставки вызова не является корректным SIP-адресом.', queue_full: 'Очередь входящих вызовов переполнена.' }
function time(value: string) { return !value || value.startsWith('0001') ? 'Ожидаем' : new Date(value).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' }) }
function Badge({ online, children }: { online: boolean; children: React.ReactNode }) { return <span className={`badge ${online ? 'badge-green' : 'badge-muted'}`}><span className="status-dot"/>{children}</span> }

function App() {
  const [state, setState] = useState<State | null>(null)
  const [tab, setTab] = useState<Tab>('doors')
  const [camerasVisited, setCamerasVisited] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const [networkError, setNetworkError] = useState(false)
  const [phone, setPhone] = useState('')
  const [code, setCode] = useState('')
  const [challenge, setChallenge] = useState('')
  const [ssoResponse, setSsoResponse] = useState('')
  const generation = useRef(0)
  const tabRefs = useRef<(HTMLButtonElement | null)[]>([])

  async function sync() {
    const revision = generation.current
    try {
      const next = await api<State>('/state')
      if (revision !== generation.current) return
      setState(next); setNetworkError(false)
    } catch { if (revision === generation.current) setNetworkError(true) }
  }
  useEffect(() => {
    let active = true
    let timer: ReturnType<typeof setTimeout>
    async function poll() { await sync(); if (active) timer = setTimeout(poll, 3000) }
    void poll()
    return () => { active = false; clearTimeout(timer); generation.current++ }
  }, [])
  async function action(fn: () => Promise<void>) {
    if (busy) return
    setBusy(true); setError(''); setNotice('')
    try { await fn(); await sync() } catch (e) { setError(e instanceof Error ? e.message : 'Ошибка') }
    finally { setBusy(false) }
  }
  function selectTab(next: Tab) { setTab(next); if (next === 'cameras') setCamerasVisited(true) }
  function tabKey(e: KeyboardEvent, index: number) {
    const next = e.key === 'Home' ? 0 : e.key === 'End' ? tabs.length - 1 : ['ArrowRight', 'ArrowDown'].includes(e.key) ? (index + 1) % tabs.length : ['ArrowLeft', 'ArrowUp'].includes(e.key) ? (index + tabs.length - 1) % tabs.length : -1
    if (next < 0) return
    e.preventDefault(); selectTab(tabs[next].id); tabRefs.current[next]?.focus()
  }
  function resetView() { setTab('doors'); setCamerasVisited(false); setCode(''); setPhone(''); setChallenge('') }
  function login(e: FormEvent) {
    e.preventDefault()
    void action(async () => {
      const result = await api<{ challenge: string; authenticated: boolean }>('/auth/login', 'POST', { phone, code, challenge })
      generation.current++; setChallenge(result.challenge)
      if (result.authenticated) resetView()
    })
  }
  async function changeMode(d: Device, mode: Mode) {
    await api(`/devices/${encodeURIComponent(d.id)}/mode`, 'PUT', { mode })
    setNotice(`${d.name}: ${labels[mode]}`)
  }
  async function openDoor(d: Device) {
    await api(`/devices/${encodeURIComponent(d.id)}/open`, 'POST')
    setNotice(`${d.name}: команда открытия принята`)
  }
  const doors = state?.devices.filter(d => !d.id.startsWith('camera/')) ?? []
  const standalone = state?.devices.filter(d => d.id.startsWith('camera/') && d.hasCamera) ?? []
  const cameras = standalone.length ? standalone : state?.devices.filter(d => d.hasCamera) ?? []
  const connected = !!state?.connected && !networkError
  const callsReady = !!state?.callReady && !networkError
  const activeRules = Object.values(state?.rules ?? {}).filter(r => r.mode !== 'off').length
  const currentTab = tabs.find(t => t.id === tab)!
  const smsForm = <form onSubmit={login} className="login-form">
    <label htmlFor="phone">Номер телефона</label>
    <input id="phone" type="tel" autoComplete="tel" placeholder="+7 900 123-45-67" required value={phone} disabled={!!challenge || busy || state?.provider === 'unconfigured'} onChange={e => setPhone(e.target.value.replace(/[\s()-]/g, ''))}/>
    {challenge && <><label htmlFor="code">Код{state?.provider === 'demo' ? ' · в демо 000000' : ' из SMS'}</label><input id="code" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" maxLength={6} required value={code} onChange={e => setCode(e.target.value)}/></>}
    <button className="button button-primary" disabled={busy || state?.provider === 'unconfigured'}>{busy ? 'Подождите…' : challenge ? 'Войти' : state?.provider === 'demo' ? 'Получить демо-код' : 'Продолжить'}<Icon name="arrow"/></button>
    {challenge && <button className="button button-quiet" type="button" disabled={busy} onClick={() => { setChallenge(''); setCode('') }}>Другой номер</button>}
  </form>
  const ssoForm = (renew: boolean) => <>
    <a className="button button-dark" href={state?.loginUrl} target="_blank" rel="noreferrer"><span className="mts-mark"/>Войти через МТС ID<Icon name="arrow"/></a>
    <p className="small muted">Войдите на сайте МТС и вставьте полученный JSON. Пароль и код SMS вводите только на сайте МТС.</p>
    <form className="login-form" onSubmit={e => { e.preventDefault(); void action(async () => { await api(renew ? '/auth/reauth' : '/auth/login', 'POST', { ssoResponse }); setSsoResponse(''); if (!renew) resetView(); generation.current++; setNotice(renew ? 'Вход обновлён. Подключаем вызовы; режимы автооткрытия сохранены.' : 'Аккаунт подключён.') }) }}><label htmlFor={renew ? 'sso-renew' : 'sso'}>Ответ после входа</label><textarea id={renew ? 'sso-renew' : 'sso'} autoComplete="off" spellCheck={false} value={ssoResponse} required maxLength={60000} onChange={e => setSsoResponse(e.target.value)} rows={4} placeholder={'{ "keycloakTokenPair": … }'}/><button className="button button-primary" disabled={busy || !ssoResponse.trim()}>{busy ? 'Подключаем…' : renew ? 'Обновить вход' : 'Подключить мой дом'}<Icon name="arrow"/></button></form>
  </>

  return <div className={`app ${state?.authenticated ? '' : 'app-guest'}`}>
    <aside className="sidebar">
      <a className="brand" href="#" onClick={e => { e.preventDefault(); selectTab('doors') }} aria-label="VDome — домофоны"><span className="brand-mark"><Icon name="door"/></span><span>VDome<span className="brand-caption">ДОСТУП К ДОМУ</span></span></a>
      {state?.authenticated && <>
        <div className="nav-label">ЛИЧНАЯ ПАНЕЛЬ</div>
        <nav className="navigation" role="tablist" aria-label="Разделы панели">
          {tabs.map((t, i) => <button key={t.id} ref={node => { tabRefs.current[i] = node }} id={`tab-${t.id}`} role="tab" aria-selected={tab === t.id} aria-controls={`panel-${t.id}`} tabIndex={tab === t.id ? 0 : -1} className={`nav-item ${tab === t.id ? 'is-active' : ''}`} onClick={() => selectTab(t.id)} onKeyDown={e => tabKey(e, i)}><Icon name={t.icon}/><span>{t.title}</span>{t.id === 'doors' && <span className="nav-count">{doors.length}</span>}</button>)}
        </nav>
        <div className="sidebar-bottom"><div className="connection-card"><span className="connection-symbol"><Icon name="signal"/></span><div><strong>{connected ? 'Дом на связи' : 'Подключаемся'}</strong><span>{callsReady ? 'Вызовы подключены' : 'Ожидаем связь с вызовами'}</span></div><span className={`status-dot ${connected ? 'is-online' : ''}`}/></div><div className="sidebar-foot"><span>Личная панель домофона</span><span>VDome / 01</span></div></div>
      </>}
    </aside>

    <main className="workspace">
      <header className="topbar"><span className="topbar-label"><Icon name="shield"/> Ваш личный доступ</span><div className="topbar-right">{state?.provider === 'demo' && <span className="demo-badge">ДЕМО</span>}{state?.authenticated ? <Badge online={connected}>{connected ? 'Подключено' : 'Нет актуальной связи'}</Badge> : <span className="muted">Добро пожаловать домой</span>}</div></header>
      <div className="page-content">
        {state?.provider === 'demo' && <div className="alert alert-info"><Icon name="bell"/><span>Демонстрационный режим: двери, вызовы и изображения синтетические.</span></div>}
        {networkError && <div role="alert" className="alert alert-error"><Icon name="signal"/><span>Нет связи с сервером. Показаны последние полученные данные.</span></div>}
        {state?.authenticated && state.syncError && <div role="alert" className="alert alert-error"><Icon name="signal"/><span>{state.syncError} {activeRules > 0 && 'Автооткрытие приостановлено до восстановления связи.'}</span>{state.needsLogin && <button className="button button-quiet button-small" onClick={() => selectTab('account')}>Обновить вход</button>}</div>}
        {state?.authenticated && connected && callsReady && (state.incomingCalls ?? []).map(call => <div key={`${call.deviceId}-${call.at}`} role="status" className="alert alert-success"><Icon name="bell"/><span><strong>Входящий вызов</strong> · {state.devices.find(d => d.id === call.deviceId)?.name ?? 'Домофон'} · {time(call.at)}</span></div>)}
        {error && <div role="alert" className="alert alert-error"><Icon name="bell"/><span>{error}</span><button className="dismiss" aria-label="Закрыть ошибку" onClick={() => setError('')}>×</button></div>}
        {notice && <div role="status" className="alert alert-success"><Icon name="check"/><span>{notice}</span><button className="dismiss" aria-label="Закрыть уведомление" onClick={() => setNotice('')}>×</button></div>}

        {!state ? <div className="empty-state"><span className="empty-icon"><Icon name="door"/></span><h1>{networkError ? 'Панель недоступна' : 'Подключаем ваш дом'}</h1><p>{networkError ? 'Проверьте, что локальный сервер запущен.' : 'Загружаем домофоны и состояние подключения…'}</p></div> : !state.authenticated ? <div className="login-layout">
          <div className="login-intro"><span className="eyebrow">ДОМ НАЧИНАЕТСЯ С ДВЕРИ</span><h1>Ваш домофон.<br/>Всегда под рукой.</h1><p>Открывайте дверь, встречайте гостей<br/>и смотрите, что происходит у подъезда.</p><div className="intercom-illustration" aria-hidden="true"><div className="speaker"><i/><i/><i/><i/><i/></div><div className="intercom-display"><Icon name="door"/><span>ДОБРО ПОЖАЛОВАТЬ</span><span className="display-ready">● ГОТОВ К ПОДКЛЮЧЕНИЮ</span></div><div className="keypad">{Array.from({ length: 12 }, (_, i) => <span key={i}>{i < 9 ? i + 1 : i === 9 ? '·' : i === 10 ? '0' : '⌑'}</span>)}</div><div className="intercom-reader"><Icon name="key"/><span>VDome</span></div></div><span className="intro-note"><Icon name="shield"/> Доступ только к домофонам вашего аккаунта</span></div>
          <section className="login-card"><span className="card-icon"><Icon name="lock"/></span><h2>Войти в панель</h2><p className="muted">Подключите свой аккаунт VDome</p>
            {state.provider === 'live' ? <>
              {ssoForm(false)}
              <details className="alternative-login"><summary>Другой способ · SMS</summary><p className="small muted">Прежний SMS API: доступность на сервере не подтверждена.</p>{smsForm}</details>
            </> : <>{state.provider === 'unconfigured' && <p className="alert alert-info">Провайдер пока не подключён.</p>}{smsForm}</>}
            <p className="login-security"><Icon name="shield"/> Пароль МТС остаётся на сайте МТС</p>
          </section>
        </div> : <>
          <div className="page-heading"><div><span className="eyebrow">ЛИЧНАЯ ПАНЕЛЬ / {currentTab.title.toLocaleUpperCase('ru-RU')}</span><h1>{currentTab.heading}</h1><p>{currentTab.subtitle}</p></div><span className="heading-symbol"><Icon name={currentTab.icon}/></span></div>

          <section id="panel-doors" role="tabpanel" aria-labelledby="tab-doors" hidden={tab !== 'doors'} tabIndex={0}>
            <div className="overview-strip"><div><span className="overview-icon"><Icon name="door"/></span><div><span>Домофоны</span><strong>{doors.length.toString().padStart(2, '0')} <small>в вашем доме</small></strong></div></div><div><span className="overview-icon"><Icon name="key"/></span><div><span>Автооткрытие</span><strong>{activeRules.toString().padStart(2, '0')} <small>активных режимов</small></strong></div></div><div><span className="overview-icon"><Icon name="refresh"/></span><div><span>Обновлено</span><strong className="time-value">{time(state.lastSync)}</strong></div></div></div>
            <div className="section-heading"><h2>Точки доступа <span>{doors.length}</span></h2><span><span className={`status-dot ${callsReady ? 'is-online' : ''}`}/>{callsReady ? 'Входящие вызовы подключены' : 'Вызовы подключаются'}</span></div>
            {doors.length === 0 ? <Empty icon="door" title="Домофоны пока не появились" text="Ожидаем список устройств вашего аккаунта."/> : <div className="door-grid">{doors.map((d, i) => {
              const mode = state.rules[d.id]?.mode ?? 'off'
              const canArm = connected && callsReady && d.canOpen && d.apiStatus === 'online'
              return <article className="door-card" key={d.id}><div className="door-card-head"><span className="device-symbol"><Icon name="door"/></span><span className="device-number">ДОМОФОН / {String(i + 1).padStart(2, '0')}</span><Badge online={d.apiStatus === 'online' && connected}>{networkError ? 'Нет связи' : statuses[d.apiStatus] ?? 'Нет данных'}</Badge></div><h3>{d.name}</h3><p className="device-address">{d.address || 'Точка доступа вашего дома'}</p><div className="device-statuses"><span><Icon name="bell"/>Вызовы: {statuses[d.sipStatus] ?? 'Нет данных'}</span><span><Icon name="lock"/>{d.doorState === 'open' ? 'Дверь открыта' : d.doorState === 'closed' ? 'Дверь закрыта' : 'Без датчика двери'}</span></div>
                <div className="auto-control"><div className="auto-label"><span><Icon name="key"/>Автооткрытие</span><span className={mode !== 'off' ? 'mode-active' : 'muted'}>{mode !== 'off' ? 'Включено' : 'Выключено'}</span></div><div className="mode-switch" role="group" aria-label={`Автооткрытие — ${d.name}`}>{(['off', 'next', 'always'] as Mode[]).map(option => <button key={option} aria-pressed={mode === option} disabled={busy || networkError || (option !== 'off' && !canArm)} className={mode === option ? 'selected' : ''} onClick={() => void action(() => changeMode(d, option))}>{labels[option]}</button>)}</div><p className="mode-description">{mode === 'next' ? 'Одна попытка при следующем входящем вызове.' : mode === 'always' ? 'Открывать при каждом вызове, пока режим включён.' : canArm ? 'Дверь может открываться сама при входящем вызове.' : 'Для автооткрытия нужно подключение к вызовам.'}</p></div>
                <button className="button unlock-button" disabled={busy || !connected || !d.canOpen || d.apiStatus !== 'online'} onClick={() => void action(() => openDoor(d))}><Icon name="key"/><span>Открыть дверь</span><Icon name="arrow"/></button>
                {state.provider === 'demo' && <button className="button button-quiet demo-call" disabled={busy || !connected || !d.canOpen} onClick={() => void action(async () => { await api(`/devices/${encodeURIComponent(d.id)}/simulate-call`, 'POST'); setNotice('Вызов создан. Результат появится после синхронизации.') })}><Icon name="bell"/>Имитировать вызов</button>}
              </article>
            })}</div>}
            <div className="footnote"><Icon name="shield"/><p>Между командами открытия — не меньше 10 секунд. Режимы работают, пока запущен сервер; при выходе из аккаунта отключаются.</p></div>
          </section>

          <section id="panel-cameras" role="tabpanel" aria-labelledby="tab-cameras" hidden={tab !== 'cameras'} tabIndex={0}>
            <div className="section-heading"><h2>Камеры дома <span>{cameras.length}</span></h2><span>Эфир запускается по кнопке</span></div>
            {cameras.length === 0 ? <Empty icon="camera" title="Камер пока нет" text="Здесь появятся камеры, доступные вашему аккаунту."/> : <div className="camera-grid">{camerasVisited && cameras.map((d, i) => <article className="camera-card" key={d.id}><div className="camera-card-heading"><span className="camera-label"><Icon name="camera"/>КАМЕРА / {String(i + 1).padStart(2, '0')}</span><span className="small muted">{d.hasStream ? 'Прямой эфир доступен' : 'Запасной кадр'}</span></div><h3>{d.name}</h3>{d.address && <p className="device-address">{d.address}</p>}<Camera device={d} active={tab === 'cameras'}/></article>)}</div>}
            <div className="footnote"><Icon name="camera"/><p>Эфир без звука, с задержкой несколько секунд. Запасной кадр загружается один раз, затем обновляется только вручную.</p></div>
          </section>

          <section id="panel-events" role="tabpanel" aria-labelledby="tab-events" hidden={tab !== 'events'} tabIndex={0}>
            <div className="section-heading"><h2>Последние события <span>{state.audit.length}</span></h2><span>Время на вашем устройстве</span></div>
            {state.audit.length === 0 ? <Empty icon="activity" title="Здесь будут вызовы и открытия" text="Входящие вызовы записываются и при выключенном автооткрытии."/> : <div className="table-card"><div className="table"><table><thead><tr><th>Время</th><th>Домофон</th><th>Способ</th><th>Результат</th></tr></thead><tbody>{state.audit.map((a, i) => <tr key={`${a.at}-${i}`}><td className="event-time">{time(a.at)}</td><td>{state.devices.find(d => d.id === a.deviceId)?.name ?? a.deviceId}</td><td><span className="event-source"><Icon name={a.action === 'auto' ? 'key' : a.action === 'incoming' ? 'bell' : 'door'}/>{a.action === 'auto' ? 'Автоматически' : a.action === 'incoming' ? 'Входящий вызов' : 'Кнопка открытия'}</span></td><td><span className={`result-pill ${a.result === 'accepted' ? 'result-success' : ''}`}>{results[a.result] ?? a.result}</span></td></tr>)}</tbody></table></div><p className="table-note">«Команда принята» — ответ сервера. Физическое состояние двери доступно только при наличии датчика.</p></div>}
          </section>

          <section id="panel-account" role="tabpanel" aria-labelledby="tab-account" hidden={tab !== 'account'} tabIndex={0}>
            {state.provider === 'live' && <article className="settings-card call-diagnostics"><div className="section-heading"><h2>Приём вызовов</h2><Icon name="bell"/></div><p>{state.needsLogin ? 'Сначала обновите вход через МТС ID.' : callStates[state.callStatus?.state] ?? 'Ожидаем подключения'}</p>{state.callStatus?.reason && <p className="account-note">{callReasons[state.callStatus.reason] ?? 'Ожидаем восстановления связи.'} {state.callStatus.httpStatus ? `HTTP ${state.callStatus.httpStatus}.` : ''} {state.callStatus.sipStatus ? `Последний ответ SIP: ${state.callStatus.sipStatus}.` : ''} {state.callStatus.wsCloseCode ? `Закрытие WS: ${state.callStatus.wsCloseCode}.` : ''}</p>}<dl className="settings-list"><div><dt>Регистрация</dt><dd>{time(state.callStatus?.registeredAt ?? '')}</dd></div><div><dt>Последний сигнал вызова</dt><dd>{time(state.callStatus?.lastInviteAt ?? '')}</dd></div><div><dt>Сигналов / новых вызовов / отклонено</dt><dd>{state.callStatus?.receivedInvites ?? 0} / {state.callStatus?.acceptedInvites ?? 0} / {state.callStatus?.rejectedInvites ?? 0}</dd></div><div><dt>Без соответствующего домофона</dt><dd>{state.callStatus?.unmappedCalls ?? 0}</dd></div></dl>{state.callStatus?.lastRejectedReason && <p role="status" className="account-note">Последнее отклонение · {time(state.callStatus.lastRejectedAt ?? '')}: {inviteRejections[state.callStatus.lastRejectedReason] ?? 'Причина неизвестна.'}</p>}<p className="small muted">Регистрация подтверждает связь с SIP-сервером. Доставку вызова нужно проверить звонком с домофона.</p></article>}
            {state.provider === 'live' && <article className="settings-card call-diagnostics"><details open={state.needsLogin || undefined}><summary>Обновить вход через МТС ID</summary><p className="account-note">Войдите в тот же аккаунт. Режимы автооткрытия и журнал сохранятся; уже начавшийся вызов не откроет дверь после обновления входа.</p>{ssoForm(true)}</details></article>}
            <div className="account-grid"><article className="settings-card"><div className="section-heading"><h2>Подключение</h2><Icon name="signal"/></div><dl className="settings-list"><div><dt>Аккаунт VDome</dt><dd className="account-id">{state.subject}</dd></div><div><dt>Связь с домом</dt><dd><Badge online={connected}>{connected ? 'Подключено' : 'Нет актуальной связи'}</Badge></dd></div><div><dt>Входящие вызовы</dt><dd><Badge online={callsReady}>{callsReady ? 'Подключены' : 'Ожидаем подключения'}</Badge></dd></div><div><dt>Обновление устройств</dt><dd>{time(state.lastSync)}</dd></div><div><dt>Сохранение сессии</dt><dd>{state.persistent ? 'Включено' : 'До перезапуска сервера'}</dd></div></dl></article><article className="settings-card"><span className="card-icon"><Icon name="shield"/></span><h2>Ваш доступ</h2><p className="muted">Токены VDome хранятся на сервере. В браузере — только cookie вашей сессии.</p>{state.provider === 'live' && <p className="account-note">Автооткрытие на вашем оборудовании ещё нужно проверить реальным входящим вызовом.</p>}{!state.persistent && <p className="account-note">После перезапуска сервера потребуется войти и включить автооткрытие заново.</p>}<button className="button button-logout" disabled={busy} onClick={() => void action(async () => { await api('/auth/logout', 'POST'); generation.current++; resetView(); setState(null); setNotice('') })}><Icon name="logout"/>Выйти из аккаунта</button><p className="small muted">При выходе все режимы автооткрытия отключатся.</p></article></div>
          </section>
        </>}
        <footer className="page-footer"><span>VDome<span className="footer-separator">/</span>Ваш дом ближе</span><span><span className="status-dot"/>Личная панель</span></footer>
      </div>
    </main>
  </div>
}
function Empty({ icon, title, text }: { icon: IconName; title: string; text: string }) { return <div className="empty-state"><span className="empty-icon"><Icon name={icon}/></span><h3>{title}</h3><p>{text}</p></div> }
createRoot(document.getElementById('root')!).render(<StrictMode><App/></StrictMode>)
