const numerals: [number, string][] = [
  [10, 'X'],
  [9, 'IX'],
  [5, 'V'],
  [4, 'IV'],
  [1, 'I'],
]

/** The tier numbering the catalog names tiers with: 7 → "VII". */
export function roman(value: number): string {
  let rest = value
  let out = ''
  for (const [size, numeral] of numerals) {
    while (rest >= size) {
      out += numeral
      rest -= size
    }
  }

  return out
}
