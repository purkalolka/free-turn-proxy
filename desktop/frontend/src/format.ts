// A client that is connected re-handshakes about every two minutes (WireGuard's
// rekey interval), so a handshake newer than a bit over that means "connected now".
export const ONLINE_WINDOW_SEC = 200

export type Presence = 'online' | 'recent' | 'idle' | 'never'

/** How live a user looks, from their last WireGuard handshake (server clock, unix seconds; 0 = never). */
export function presence(lastHandshake: number, serverNow: number): Presence {
  if (!lastHandshake) return 'never'
  const ago = serverNow - lastHandshake
  if (ago < ONLINE_WINDOW_SEC) return 'online'
  if (ago < 3600) return 'recent'
  return 'idle'
}

/** "онлайн" / "12 мин назад" / "3 ч назад" / "5 дн. назад" / "не подключался". */
export function formatLastSeen(lastHandshake: number, serverNow: number): string {
  if (!lastHandshake) return 'не подключался'
  const ago = Math.max(0, serverNow - lastHandshake)
  if (ago < ONLINE_WINDOW_SEC) return 'онлайн'
  const min = Math.floor(ago / 60)
  if (min < 60) return `${min} мин назад`
  const h = Math.floor(min / 60)
  if (h < 24) return `${h} ч назад`
  const d = Math.floor(h / 24)
  if (d < 60) return `${d} дн. назад`
  return new Date(lastHandshake * 1000).toLocaleDateString('ru-RU')
}

/** Exact local time of the last handshake, for a tooltip. */
export function exactTime(lastHandshake: number): string {
  return lastHandshake ? new Date(lastHandshake * 1000).toLocaleString('ru-RU') : ''
}

export function formatBytes(n: number): string {
  if (!n || n < 0) return '0 Б'
  const units = ['Б', 'КБ', 'МБ', 'ГБ', 'ТБ']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}
