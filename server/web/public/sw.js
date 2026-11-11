/* Boop service worker: shows browser push notifications and focuses the app
   when one is clicked. The payload is built by server/internal/delivery/webpush.go. */
self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', (event) => event.waitUntil(self.clients.claim()))

self.addEventListener('push', (event) => {
  let data = {}
  try {
    data = event.data ? event.data.json() : {}
  } catch {
    data = { title: 'Boop', body: event.data ? event.data.text() : '' }
  }
  const title = data.title || 'Boop'
  const options = {
    body: data.body || '',
    tag: data.event_id || undefined,
    renotify: Boolean(data.event_id),
    data: { url: data.url || '/' },
    icon: '/icon-192.png',
    badge: '/icon-192.png',
    timestamp: Date.now(),
    actions: (data.actions || []).slice(0, 2).map((a, i) => ({ action: 'action-' + i, title: a.label })),
  }
  event.waitUntil(self.registration.showNotification(title, options))
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const action = event.action ? (event.notification.actions || [])[Number(event.action.split('-')[1])] : null
  const target = action ? undefined : (event.notification.data && event.notification.data.url) || '/'
  event.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((clientList) => {
      for (const client of clientList) {
        if ('focus' in client) return client.focus()
      }
      return self.clients.openWindow(target || '/')
    }),
  )
})
