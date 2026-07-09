export function cloneItems<T extends object>(items: T[]) {
  return items.map((item) => ({ ...item }))
}

export function clamp(value: number, min: number, max: number) {
  return Math.min(Math.max(value, min), max)
}

export function parsedNumber(value: string, fallback: number) {
  const next = Number(value)
  return Number.isFinite(next) ? next : fallback
}
