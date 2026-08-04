package dormancy

import (
	"math"
	"time"
)

// coldStartCadence is the gap a run is credited with before it has shown enough
// of its own cadence to learn from.
//
// It is the only duration constant in this package and it is deliberately NOT a
// policy. It says "this run has not yet told us how it paces itself", never
// "this long is too long for the work". A duration that meant the latter would
// be the exact defect this design replaces: a wall clock that cannot know
// whether a growth is stopped or merely slow, and that gets the answer wrong in
// both directions depending on the task it happens to be given.
//
// Once minLearnedIntervals gaps have been observed it is never consulted again
// for that run, so no growth is ever judged against a number nobody measured
// from it. It cannot end anything either way: suspicion only ever raises
// verbosity.
const coldStartCadence = 30 * time.Second

// minLearnedIntervals is how many inter-arrival gaps a run must show before its
// own cadence replaces the cold-start value. One gap has no spread at all and
// two have none worth the name; three is the smallest count from which a
// deviation carries information.
const minLearnedIntervals = 3

// cadenceDeviations widens a run's envelope past its mean gap by this many
// standard deviations of that run's OWN gaps. It is a dimensionless multiple of
// an observed spread, not a duration: a growth that streams every few
// milliseconds and one that thinks for minutes between tool calls both get an
// envelope scaled to what they have actually been doing.
const cadenceDeviations = 3.0

// reportingSuspicion is the level at which dormancy is reported. Suspicion is
// counted in envelopes of silence — "this run has now been quiet for five times
// the widest gap it has ever shown" — so this carries no unit and no assumption
// about how long the task ought to take. Crossing it publishes a report and
// does nothing else.
const reportingSuspicion = 4.0

// cadence accumulates one run's observed inter-arrival gaps. It keeps a running
// mean and variance (Welford, so no interval history is retained) plus the
// widest gap seen, which is what stops a single long-but-survived pause from
// being treated as evidence a second one is fatal.
type cadence struct {
	count int
	mean  float64 // seconds
	m2    float64 // sum of squared deviations, for the running variance
	max   float64 // seconds
}

// observe folds one gap between consecutive signs of life into the run's
// distribution. A non-positive gap is not an observation — two events sharing a
// timestamp say nothing about pacing — and is dropped rather than recorded as a
// zero, which would drag the mean toward an interval the run never showed.
func (c *cadence) observe(gap time.Duration) {
	if c == nil || gap <= 0 {
		return
	}

	seconds := gap.Seconds()
	c.count++
	delta := seconds - c.mean
	c.mean += delta / float64(c.count)
	c.m2 += delta * (seconds - c.mean)
	if seconds > c.max {
		c.max = seconds
	}
}

// envelope returns the widest silence this run should be expected to show
// without anything being unusual, and whether that figure was learned from the
// run itself. A false second return means the cold-start value is standing in
// because the run has not yet shown enough of its own pacing — every surface
// that reports a level must say which of the two it used, because they mean
// entirely different things about how much is known.
func (c *cadence) envelope() (time.Duration, bool) {
	if c == nil || c.count < minLearnedIntervals {
		return coldStartCadence, false
	}

	stddev := 0.0
	if c.count > 1 {
		stddev = math.Sqrt(c.m2 / float64(c.count-1))
	}

	widest := c.mean + cadenceDeviations*stddev
	if c.max > widest {
		widest = c.max
	}

	return time.Duration(widest * float64(time.Second)), true
}

// suspicionFor converts a silence into a suspicion level, in units of the run's
// own envelope. Silence inside the envelope is zero — a run behaving the way it
// has behaved all along is not suspicious, however long that is in seconds.
// Past the envelope it rises linearly, so the level answers "how many times
// more silence than this run has ever shown", which is a question the run's own
// history can answer. It never saturates and never crosses into a verdict.
func suspicionFor(silence, envelope time.Duration) float64 {
	if silence <= 0 || envelope <= 0 {
		return 0
	}

	ratio := float64(silence) / float64(envelope)
	if ratio <= 1 {
		return 0
	}

	return ratio - 1
}
