// Locale-aware dates for the console (spec 10 §4: ru and en).
export function useFormat() {
  const { locale } = useI18n()

  // "5 min ago" / "5 минут назад".
  function timeAgo(iso: string): string {
    const rtf = new Intl.RelativeTimeFormat(locale.value, { numeric: 'auto' })
    const seconds = Math.round((new Date(iso).getTime() - Date.now()) / 1000)
    const units: [Intl.RelativeTimeFormatUnit, number][] = [['day', 86400], ['hour', 3600], ['minute', 60]]
    for (const [unit, size] of units) {
      if (Math.abs(seconds) >= size) {
        return rtf.format(Math.round(seconds / size), unit)
      }
    }
    return rtf.format(0, 'second')
  }

  // "24 окт., 14:36" / "Oct 24, 14:36".
  function dateTime(iso: string): string {
    return new Date(iso).toLocaleString(locale.value, {
      day: 'numeric',
      month: 'short',
      hour: '2-digit',
      minute: '2-digit',
      hour12: false
    })
  }

  return { timeAgo, dateTime }
}
