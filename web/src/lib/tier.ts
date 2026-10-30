export type TierBand = 'bronze' | 'silver' | 'gold' | 'purple'

const bands: TierBand[] = ['bronze', 'silver', 'gold', 'purple']

/**
 * The band a tier's badge is drawn in on GitLab: three tiers to a band,
 * lowest first. Mirrors tierMarker in internal/catalog/badge.go.
 */
export function tierBand(tier: number): TierBand {
  return bands[Math.floor((Math.max(tier, 1) - 1) / 3)] ?? 'purple'
}
