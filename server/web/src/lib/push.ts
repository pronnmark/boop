import { api } from './api'

export type PushState = 'unsupported' | 'denied' | 'off' | 'on'

/** Register the service worker that receives browser push. */
export async function registerWorker(): Promise<ServiceWorkerRegistration | null> {
  if (!('serviceWorker' in navigator)) return null
  try {
    return await navigator.serviceWorker.register('/sw.js', { scope: '/' })
  } catch {
    return null
  }
}

function keyBytes(base64: string): Uint8Array {
  const padded = (base64 + '='.repeat((4 - (base64.length % 4)) % 4)).replace(/-/g, '+').replace(/_/g, '/')
  const raw = atob(padded)
  const out = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out
}

/** Current state of this browser's push subscription. */
export async function pushState(): Promise<PushState> {
  if (!('serviceWorker' in navigator) || !('PushManager' in window)) return 'unsupported'
  if (Notification.permission === 'denied') return 'denied'
  const reg = await navigator.serviceWorker.getRegistration()
  const sub = await reg?.pushManager.getSubscription()
  return sub && Notification.permission === 'granted' ? 'on' : 'off'
}

/** Ask for notification permission and store the subscription on the server. */
export async function enablePush(): Promise<PushState> {
  if (!('serviceWorker' in navigator) || !('PushManager' in window)) return 'unsupported'
  const reg = await registerWorker()
  if (!reg) return 'unsupported'
  const permission = await Notification.requestPermission()
  if (permission !== 'granted') return permission === 'denied' ? 'denied' : 'off'
  const { public_key } = await api.webpushKey()
  const existing = await reg.pushManager.getSubscription()
  const sub =
    existing ??
    (await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(public_key) }))
  const json = sub.toJSON() as { endpoint?: string; keys?: { p256dh?: string; auth?: string } }
  if (!json.endpoint || !json.keys?.p256dh || !json.keys.auth) return 'off'
  await api.webpushSubscribe({ endpoint: json.endpoint, keys: { p256dh: json.keys.p256dh, auth: json.keys.auth } })
  return 'on'
}

/** Drop the subscription locally and on the server. */
export async function disablePush(): Promise<PushState> {
  const reg = await navigator.serviceWorker.getRegistration()
  const sub = await reg?.pushManager.getSubscription()
  if (sub) {
    await api.webpushUnsubscribe(sub.endpoint).catch(() => undefined)
    await sub.unsubscribe()
  }
  return 'off'
}
