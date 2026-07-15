// Package scheduler models .tendril/schedules.yaml: cron-timed entries that
// grow a Sequence or a Sprout. This slice provides the stdlib-only cron
// parser and the config loader; wiring into serve and actual firing land in
// later slices.
package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// fieldMask records which values of a cron field match, one bit per value.
type fieldMask uint64

func (m fieldMask) has(v int) bool {
	return m&(1<<uint(v)) != 0
}

// Schedule is a parsed 5-field cron expression
// (minute hour day-of-month month day-of-week).
type Schedule struct {
	minute fieldMask
	hour   fieldMask
	dom    fieldMask
	month  fieldMask
	dow    fieldMask

	// Standard cron semantics: when both day-of-month and day-of-week are
	// restricted (neither is "*"), a day matches if EITHER field matches.
	domStar bool
	dowStar bool

	spec string
}

// String returns the original cron spec the Schedule was parsed from.
func (s Schedule) String() string {
	return s.spec
}

// fieldDef bounds one of the five cron fields.
type fieldDef struct {
	name string
	min  int
	max  int
	// fold7 lets day-of-week accept 7 as an alias for Sunday (0), like
	// Vixie cron.
	fold7 bool
}

var fieldDefs = [5]fieldDef{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day-of-month", min: 1, max: 31},
	{name: "month", min: 1, max: 12},
	{name: "day-of-week", min: 0, max: 7, fold7: true},
}

// Parse parses a 5-field cron spec supporting "*", lists (","), ranges ("-"),
// and steps ("*/n", "a-b/n"). Day-of-week accepts 0-7 with both 0 and 7
// meaning Sunday.
func Parse(spec string) (Schedule, error) {
	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return Schedule{}, fmt.Errorf("cron spec %q: want 5 fields (minute hour day-of-month month day-of-week), got %d", spec, len(fields))
	}

	var masks [5]fieldMask
	for i, field := range fields {
		mask, err := parseField(field, fieldDefs[i])
		if err != nil {
			return Schedule{}, fmt.Errorf("cron spec %q: %w", spec, err)
		}
		masks[i] = mask
	}

	return Schedule{
		minute:  masks[0],
		hour:    masks[1],
		dom:     masks[2],
		month:   masks[3],
		dow:     masks[4],
		domStar: fields[2] == "*",
		dowStar: fields[4] == "*",
		spec:    spec,
	}, nil
}

func parseField(field string, def fieldDef) (fieldMask, error) {
	var mask fieldMask
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return 0, fmt.Errorf("%s field %q: empty list item", def.name, field)
		}
		m, err := parsePart(part, def)
		if err != nil {
			return 0, err
		}
		mask |= m
	}
	return mask, nil
}

func parsePart(part string, def fieldDef) (fieldMask, error) {
	base, stepStr, hasStep := strings.Cut(part, "/")

	step := 1
	if hasStep {
		n, err := strconv.Atoi(stepStr)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("%s field %q: step must be a positive integer", def.name, part)
		}
		step = n
	}

	var lo, hi int
	switch {
	case base == "*":
		lo, hi = def.min, def.max
	case strings.Contains(base, "-"):
		loStr, hiStr, _ := strings.Cut(base, "-")
		var err error
		if lo, err = strconv.Atoi(loStr); err != nil {
			return 0, fmt.Errorf("%s field %q: invalid range start %q", def.name, part, loStr)
		}
		if hi, err = strconv.Atoi(hiStr); err != nil {
			return 0, fmt.Errorf("%s field %q: invalid range end %q", def.name, part, hiStr)
		}
		if lo > hi {
			return 0, fmt.Errorf("%s field %q: range start %d exceeds end %d", def.name, part, lo, hi)
		}
	default:
		if hasStep {
			return 0, fmt.Errorf("%s field %q: a step requires \"*\" or a range", def.name, part)
		}
		v, err := strconv.Atoi(base)
		if err != nil {
			return 0, fmt.Errorf("%s field %q: not a number", def.name, part)
		}
		lo, hi = v, v
	}

	if lo < def.min || hi > def.max {
		return 0, fmt.Errorf("%s field %q: values must be within %d-%d", def.name, part, def.min, def.max)
	}

	var mask fieldMask
	for v := lo; v <= hi; v += step {
		bit := v
		if def.fold7 && bit == 7 {
			bit = 0
		}
		mask |= 1 << uint(bit)
	}
	return mask, nil
}

// nextScanYears bounds how far Next searches before giving up on impossible
// specs like "0 0 30 2 *" (February 30th never exists).
const nextScanYears = 5

// Next returns the first fire time strictly after the given instant, in that
// instant's location (local wall-clock cron, like Vixie). It returns the zero
// Time when no fire time exists within nextScanYears.
func (s Schedule) Next(after time.Time) time.Time {
	loc := after.Location()
	// Start at the following minute boundary: cron fires on whole minutes,
	// strictly after the reference instant.
	t := time.Date(after.Year(), after.Month(), after.Day(), after.Hour(), after.Minute(), 0, 0, loc).Add(time.Minute)
	limit := after.AddDate(nextScanYears, 0, 1)

	for t.Before(limit) {
		if !s.month.has(int(t.Month())) {
			// Jump to the first minute of the next month.
			t = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, 1, 0)
			continue
		}
		if !s.dayMatches(t) {
			// Jump to midnight of the next day.
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
			continue
		}
		if !s.hour.has(t.Hour()) {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, loc).Add(time.Hour)
			continue
		}
		if !s.minute.has(t.Minute()) {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

// dayMatches applies standard cron day semantics: when both day-of-month and
// day-of-week are restricted, either one matching is enough.
func (s Schedule) dayMatches(t time.Time) bool {
	domOK := s.dom.has(t.Day())
	dowOK := s.dow.has(int(t.Weekday()))
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dowOK
	case s.dowStar:
		return domOK
	default:
		return domOK || dowOK
	}
}
