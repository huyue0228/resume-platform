function hashSchoolTag(value) {
  let hash = 2166136261
  for (const character of value) {
    hash ^= character.codePointAt(0)
    hash = Math.imul(hash, 16777619) >>> 0
  }
  hash ^= hash >>> 16
  hash = Math.imul(hash, 0x85ebca6b) >>> 0
  hash ^= hash >>> 13
  hash = Math.imul(hash, 0xc2b2ae35) >>> 0
  return (hash ^ (hash >>> 16)) >>> 0
}

export function schoolTagColor(value) {
  const label = String(value ?? '').trim()
  if (!label) return undefined
  const hash = hashSchoolTag(label)
  const hue = Math.floor((hash / 0x100000000) * 360)
  const saturation = 62 + ((hash >>> 8) % 13)
  const lightness = 36 + ((hash >>> 16) % 9)
  return `hsl(${hue} ${saturation}% ${lightness}%)`
}
