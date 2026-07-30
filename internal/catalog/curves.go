package catalog

// Curve maps a zero-based tier index to the counter value that tier
// requires.
//
// The four standard curves are ported from the VS Code Achievements
// extension's StackingTemplates, which this catalog's difficulty pacing is
// modelled on. They are built from powers of 2, 5, and 10 so thresholds
// land on round, legible numbers (5, 50, 500) instead of whatever an
// exponential happens to produce, and so consecutive tiers alternate
// between a 2x and a 5x step rather than jumping by a full order of
// magnitude every time.
type Curve func(tier int64) int64

// The bases the curves are built from. Alternating a factor of two with a
// factor of five is what makes consecutive tiers step 1, 5, 10, 50 rather
// than by a full order of magnitude each time.
const (
	twofold  = 2
	fivefold = 5
	tenfold  = 10
	// nudge is the extra factor the infernal curve applies at its second
	// tier alone, which is what gives it three tiers below ten instead of
	// the hard curve's two.
	nudge = 3
	// tiersPerFactor is how many tiers each curve takes to pick up another
	// factor: they alternate, so a factor of two and a factor of five are
	// applied on opposite tiers.
	tiersPerFactor = 2
	// mediumHeadStart lifts the medium curve one factor above the hard one,
	// so its first tier asks for 2 rather than 1.
	mediumHeadStart = 2
)

// StandardEasyCurve is 10^t: 1, 10, 100, 1000, ... Reserved for criteria
// that accumulate so fast that a finer curve would award several tiers from
// a single event.
func StandardEasyCurve(tier int64) int64 {
	return pow(tenfold, tier)
}

// StandardMediumCurve is 2^((t+2)/2) * 5^((t+1)/2): 2, 10, 20, 100, 200,
// ... It starts at 2, so the first tier still asks for something.
func StandardMediumCurve(tier int64) int64 {
	return pow(twofold, (tier+mediumHeadStart)/tiersPerFactor) * pow(fivefold, (tier+1)/tiersPerFactor)
}

// StandardHardCurve is 2^(t/2) * 5^((t+1)/2): 1, 5, 10, 50, 100, ... The
// default for criteria a user racks up steadily over months.
func StandardHardCurve(tier int64) int64 {
	return pow(twofold, tier/tiersPerFactor) * pow(fivefold, (tier+1)/tiersPerFactor)
}

// StandardInfernalCurve is 1, 3, 5, 10, 50, 100, 500, ... It opens gently
// (three tiers below ten) and then climbs as steeply as the hard curve,
// which suits criteria most users touch a handful of times and a few users
// touch constantly.
func StandardInfernalCurve(tier int64) int64 {
	second := int64(1)
	if tier == 1 {
		second = nudge
	}

	return pow(twofold, max(0, (tier-1)/tiersPerFactor)) * second * pow(fivefold, tier/tiersPerFactor)
}

// Steps returns a Curve reading tier thresholds from an explicit list,
// for criteria whose milestones are meaningful numbers rather than points
// on a curve (a streak's week, month, and year marks). Tiers past the end
// of the list repeat the last value, so a template can't silently generate
// unreachable tiers by outrunning its own list.
func Steps(values ...int64) Curve {
	return func(tier int64) int64 {
		if tier < 0 {
			return values[0]
		}

		if tier >= int64(len(values)) {
			return values[len(values)-1]
		}

		return values[tier]
	}
}

// pow returns base**exponent for non-negative exponents. The standard
// library only offers math.Pow, which works in float64 and would need
// rounding back to an integer threshold.
func pow(base, exponent int64) int64 {
	result := int64(1)

	for range exponent {
		result *= base
	}

	return result
}
