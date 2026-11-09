import { test, expect, type Page, type BrowserContext } from '@playwright/test'

let demoCookies: Awaited<ReturnType<BrowserContext['cookies']>> | undefined
async function demoLogin(page: Page) {
  if (demoCookies) { await page.context().addCookies(demoCookies); await page.goto('/'); return }
  const state = await page.request.get('/api/state').then(r => r.json())
  // Never run unlock/mode checks against a real provider.
  expect(state.provider).toBe('demo')
  const phone = `79${Math.floor(Math.random() * 1e9).toString().padStart(9, '0')}`
  const headers = { 'X-VDome-Request': '1' }
  const first = await page.request.post('/api/auth/login', { headers, data: { phone } }).then(r => r.json())
  const second = await page.request.post('/api/auth/login', { headers, data: { phone, code: '000000', challenge: first.challenge } })
  expect(second.ok()).toBeTruthy()
  demoCookies = await page.context().cookies()
  await page.goto('/')
  await expect(page.getByRole('tab', { name: 'Домофоны' })).toBeVisible()
}

test('login, one-shot auto opening and journal work in the demo', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Войти в панель' })).toBeVisible()
  const state = await page.request.get('/api/state').then(r => r.json())
  expect(state.provider).toBe('demo')
  await page.getByLabel('Номер телефона').fill(`79${Math.floor(Math.random() * 1e9).toString().padStart(9, '0')}`)
  await page.getByRole('button', { name: 'Получить демо-код' }).click()
  await page.getByLabel('Код · в демо 000000').fill('000000')
  await page.getByRole('button', { name: 'Войти', exact: true }).click()
  const modes = page.getByRole('group', { name: 'Автооткрытие — Подъезд', exact: true })
  await expect(modes.getByRole('button', { name: 'Один вызов' })).toBeEnabled()
  await modes.getByRole('button', { name: 'Один вызов' }).click()
  await expect(modes.getByRole('button', { name: 'Один вызов' })).toHaveAttribute('aria-pressed', 'true')
  await page.locator('.door-card').filter({ has: page.getByRole('heading', { name: 'Подъезд', exact: true }) }).getByRole('button', { name: 'Имитировать вызов' }).click()
  await expect(modes.getByRole('button', { name: 'Выключено' })).toHaveAttribute('aria-pressed', 'true')
  await page.getByRole('tab', { name: 'Журнал' }).click()
  await expect(page.getByRole('cell', { name: 'Команда принята' })).toBeVisible()
  await page.screenshot({ path: `test-results/journal-${test.info().project.name}.png`, fullPage: true })
})

test('tabs preserve a single snapshot until manual refresh', async ({ page }) => {
  await demoLogin(page)
  await expect(page.getByRole('heading', { name: 'Подъезд', exact: true })).toBeVisible()
  await page.screenshot({ path: `test-results/doors-${test.info().project.name}.png`, fullPage: true })
  let requests = 0
  page.on('request', req => { if (new URL(req.url()).pathname.endsWith('/snapshot')) requests++ })
  await page.getByRole('tab', { name: 'Камеры' }).click()
  await expect(page.getByRole('img', { name: 'Камера: Подъезд' })).toBeVisible()
  await page.waitForTimeout(6500)
  expect(requests).toBe(1)
  await page.getByRole('tab', { name: 'Домофоны' }).click()
  await page.getByRole('tab', { name: 'Камеры' }).click()
  await expect(page.getByRole('img', { name: 'Камера: Подъезд' })).toBeVisible()
  expect(requests).toBe(1)
  await page.getByRole('button', { name: 'Обновить кадр' }).click()
  await expect.poll(() => requests).toBe(2)
  await page.screenshot({ path: `test-results/cameras-${test.info().project.name}.png`, fullPage: true })
  await page.getByRole('tab', { name: 'Аккаунт' }).click()
  await expect(page.getByRole('heading', { name: 'Ваше подключение' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Выйти из аккаунта' })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.getByRole('tab', { name: 'Домофоны' }).focus()
  await page.keyboard.press('ArrowRight')
  await expect(page.getByRole('tab', { name: 'Камеры' })).toBeFocused()
  await expect(page.getByRole('tab', { name: 'Камеры' })).toHaveAttribute('aria-selected', 'true')
})

test('leaving cameras cancels even a stream that starts late', async ({ page }) => {
  await demoLogin(page)
  await page.route('**/api/state', async route => {
    const response = await route.fetch()
    const state = await response.json()
    for (const d of state.devices) if (d.id === 'entrance') d.hasStream = true
    await route.fulfill({ response, json: state })
  })
  await page.reload()
  let release!: () => void
  const hold = new Promise<void>(resolve => { release = resolve })
  let started = false
  let stopped = 0
  await page.route('**/api/devices/entrance/stream', async route => {
    started = true
    await hold
    await route.fulfill({ json: { id: 'test-stream', url: '/api/streams/test-stream/index.m3u8' } })
  })
  await page.route('**/api/streams/test-stream', async route => { if (route.request().method() === 'DELETE') stopped++; await route.fulfill({ json: { ok: true } }) })
  await page.getByRole('tab', { name: 'Камеры' }).click()
  await page.getByRole('button', { name: 'Смотреть эфир' }).click()
  await expect.poll(() => started).toBe(true)
  await page.getByRole('tab', { name: 'Домофоны' }).click()
  release()
  await expect.poll(() => stopped).toBe(1)
  await page.getByRole('tab', { name: 'Камеры' }).click()
  await expect(page.getByRole('button', { name: 'Смотреть эфир' })).toBeEnabled()
})

test('expired login can be renewed and incoming calls are visible', async ({ page }) => {
  await demoLogin(page)
  let renewed = false
  let ringing = true
  const at = new Date().toISOString()
  await page.route('**/api/state', async route => {
    const response = await route.fetch()
    const state = await response.json()
    state.provider = 'live'
    state.needsLogin = !renewed
    state.connected = renewed
    state.callReady = renewed
    state.syncError = renewed ? '' : 'МТС отклонил сессию. Обновите вход через МТС ID.'
    state.loginUrl = 'https://login.mts.ru/'
    state.rules = { entrance: { mode: 'always', armedAt: at } }
    state.incomingCalls = renewed && ringing ? [{ deviceId: 'entrance', at }] : []
    state.callStatus = { state: renewed ? 'registered' : 'waiting', receivedInvites: renewed ? 1 : 0, acceptedInvites: renewed ? 1 : 0, rejectedInvites: 0, unmappedCalls: 0, registeredAt: renewed ? at : '', lastInviteAt: renewed ? at : '' }
    state.audit = renewed ? [{ at, deviceId: 'entrance', action: 'incoming', result: 'received' }] : []
    await route.fulfill({ response, json: state })
  })
  await page.route('**/api/auth/reauth', async route => {
    expect(route.request().method()).toBe('POST')
    expect(route.request().postDataJSON()).toEqual({ ssoResponse: '{"fixture":"callback"}' })
    renewed = true
    await route.fulfill({ json: { authenticated: true } })
  })
  await page.reload()
  await page.getByRole('button', { name: 'Обновить вход', exact: true }).click()
  await page.getByLabel('Ответ после входа').fill('{"fixture":"callback"}')
  await page.locator('form').getByRole('button', { name: 'Обновить вход', exact: true }).click()
  await expect(page.getByText('Вход обновлён.', { exact: false })).toBeVisible()
  await expect(page.getByText('Готовы принимать вызовы', { exact: true })).toBeVisible()
  const banner = page.getByRole('status').filter({ hasText: 'Входящий вызов' })
  await expect(banner).toBeVisible()
  ringing = false
  await expect(banner).toHaveCount(0)
  await page.getByRole('tab', { name: 'Домофоны' }).click()
  await expect(page.getByRole('group', { name: 'Автооткрытие — Подъезд', exact: true }).getByRole('button', { name: 'Каждый вызов' })).toHaveAttribute('aria-pressed', 'true')
  await page.getByRole('tab', { name: 'Журнал' }).click()
  await expect(page.getByRole('cell', { name: 'Вызов получен' })).toBeVisible()
})
