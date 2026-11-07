export type Mode = 'off' | 'next' | 'always'
export interface Device { id: string; name: string; address: string; apiStatus: string; sipStatus: string; doorState: string; canOpen: boolean; hasCamera: boolean; hasStream: boolean }
export interface Rule { mode: Mode; armedAt: string; lastAttempt?: string }
export interface Audit { at: string; deviceId: string; callId?: string; action: string; result: string }
export interface State { authenticated: boolean; provider: string; subject?: string; persistent: boolean; connected: boolean; callReady: boolean; loginUrl?: string; lastSync: string; devices: Device[]; rules: Record<string, Rule>; audit: Audit[] }
export async function api<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch(`/api${path}`, {
    method, credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', 'X-VDome-Request': '1' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const data: unknown = await response.json()
  if (!response.ok) throw new Error((data as {error?: string}).error ?? 'Не удалось выполнить запрос')
  return data as T
}
