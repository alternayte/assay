const timeFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

export function formatTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : timeFormat.format(d);
}

const relative = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

/** Formats a past time relative to now, such as "5 minutes ago". */
export function formatAgo(iso: string, now = Date.now()): string {
  const d = new Date(iso).getTime();
  if (Number.isNaN(d)) return iso;
  const seconds = Math.round((d - now) / 1000);
  const units: [Intl.RelativeTimeFormatUnit, number][] = [
    ["day", 86400],
    ["hour", 3600],
    ["minute", 60],
  ];
  for (const [unit, size] of units) {
    if (Math.abs(seconds) >= size) return relative.format(Math.round(seconds / size), unit);
  }
  return relative.format(seconds, "second");
}
