package scheduler

import (
	"testing"
	"time"
)

func TestParseValidSpecs(t *testing.T) {
	specs := []string{
		"* * * * *",
		"0 19 * * 1-5",
		"30 21 * * *",
		"*/15 * * * *",
		"0-30/10 4 1,15 1-6/2 0",
		"59 23 31 12 7",
		"5,10,15 * * * *",
		"0 0,12 1 */2 *",
		"0 0 * * 0-7",
		"  0   19  *  *  1-5  ", // extra whitespace between fields
	}
	for _, spec := range specs {
		if _, err := Parse(spec); err != nil {
			t.Errorf("Parse(%q) error = %v, want nil", spec, err)
		}
	}
}

func TestParseInvalidSpecs(t *testing.T) {
	specs := []string{
		"",                 // no fields
		"* * * *",          // 4 fields
		"* * * * * *",      // 6 fields
		"60 * * * *",       // minute out of range
		"* 24 * * *",       // hour out of range
		"* * 0 * *",        // day-of-month below range
		"* * 32 * *",       // day-of-month above range
		"* * * 0 *",        // month below range
		"* * * 13 *",       // month above range
		"* * * * 8",        // day-of-week above range
		"a * * * *",        // not a number
		"*/0 * * * *",      // zero step
		"*/-2 * * * *",     // negative step
		"*/x * * * *",      // non-numeric step
		"5/2 * * * *",      // step on a single value
		"10-5 * * * *",     // inverted range
		"1,,2 * * * *",     // empty list item
		"1--2 * * * *",     // malformed range
		"-5 * * * *",       // dangling range
		"1-70 * * * *",     // range end out of bounds
		"0 19 * * mon-fri", // names unsupported
	}
	for _, spec := range specs {
		if _, err := Parse(spec); err == nil {
			t.Errorf("Parse(%q) error = nil, want error", spec)
		}
	}
}

func TestNext(t *testing.T) {
	// 2026-07-13 is a Monday.
	at := func(y int, mo time.Month, d, h, mi int) time.Time {
		return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
	}

	tests := []struct {
		name  string
		spec  string
		after time.Time
		want  time.Time
	}{
		{
			name:  "every minute rounds up past seconds",
			spec:  "* * * * *",
			after: time.Date(2026, time.July, 13, 10, 30, 30, 0, time.UTC),
			want:  at(2026, time.July, 13, 10, 31),
		},
		{
			name:  "strictly after an exact match",
			spec:  "* * * * *",
			after: at(2026, time.July, 13, 10, 30),
			want:  at(2026, time.July, 13, 10, 31),
		},
		{
			name:  "top of next hour",
			spec:  "0 * * * *",
			after: at(2026, time.July, 13, 10, 30),
			want:  at(2026, time.July, 13, 11, 0),
		},
		{
			name:  "hour already passed rolls to next day",
			spec:  "15 9 * * *",
			after: at(2026, time.July, 13, 10, 0),
			want:  at(2026, time.July, 14, 9, 15),
		},
		{
			name:  "midnight rollover",
			spec:  "0 0 * * *",
			after: at(2026, time.July, 13, 5, 0),
			want:  at(2026, time.July, 14, 0, 0),
		},
		{
			name:  "31st skips short months",
			spec:  "0 0 31 * *",
			after: at(2026, time.June, 15, 0, 0),
			want:  at(2026, time.July, 31, 0, 0),
		},
		{
			name:  "31st skips February",
			spec:  "0 0 31 * *",
			after: time.Date(2027, time.January, 31, 0, 30, 0, 0, time.UTC),
			want:  at(2027, time.March, 31, 0, 0),
		},
		{
			name:  "year rollover",
			spec:  "0 0 1 1 *",
			after: at(2026, time.March, 1, 0, 0),
			want:  at(2027, time.January, 1, 0, 0),
		},
		{
			name:  "weekday range skips the weekend",
			spec:  "0 19 * * 1-5",
			after: at(2026, time.July, 11, 20, 0), // Saturday evening
			want:  at(2026, time.July, 13, 19, 0), // Monday
		},
		{
			name:  "weekday range fires same day when due",
			spec:  "0 19 * * 1-5",
			after: at(2026, time.July, 13, 8, 0), // Monday morning
			want:  at(2026, time.July, 13, 19, 0),
		},
		{
			name:  "dow 7 is Sunday",
			spec:  "0 12 * * 7",
			after: at(2026, time.July, 13, 0, 0), // Monday
			want:  at(2026, time.July, 19, 12, 0),
		},
		{
			name:  "step minutes",
			spec:  "*/15 * * * *",
			after: at(2026, time.July, 13, 10, 16),
			want:  at(2026, time.July, 13, 10, 30),
		},
		{
			name:  "range with step wraps to next hour",
			spec:  "10-40/10 * * * *",
			after: at(2026, time.July, 13, 10, 41),
			want:  at(2026, time.July, 13, 11, 10),
		},
		{
			name:  "minute list",
			spec:  "5,20 * * * *",
			after: at(2026, time.July, 13, 10, 6),
			want:  at(2026, time.July, 13, 10, 20),
		},
		{
			name:  "dom OR dow when both restricted",
			spec:  "0 0 13 * 5",
			after: at(2026, time.July, 13, 1, 0), // the 13th, past midnight
			want:  at(2026, time.July, 17, 0, 0), // Friday wins before Aug 13
		},
		{
			name:  "month list rolls across the year",
			spec:  "0 6 1 3,9 *",
			after: at(2026, time.October, 2, 0, 0),
			want:  at(2027, time.March, 1, 6, 0),
		},
		{
			name:  "leap day waits for a leap year",
			spec:  "0 0 29 2 *",
			after: at(2026, time.March, 1, 0, 0),
			want:  at(2028, time.February, 29, 0, 0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, err := Parse(tt.spec)
			if err != nil {
				t.Fatalf("Parse(%q) error = %v", tt.spec, err)
			}
			got := sched.Next(tt.after)
			if !got.Equal(tt.want) {
				t.Fatalf("Next(%v) = %v, want %v", tt.after, got, tt.want)
			}
		})
	}
}

func TestNextImpossibleSpecReturnsZero(t *testing.T) {
	sched, err := Parse("0 0 30 2 *") // February 30th never exists
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	got := sched.Next(time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC))
	if !got.IsZero() {
		t.Fatalf("Next() = %v, want zero time", got)
	}
}

func TestNextKeepsLocation(t *testing.T) {
	loc := time.FixedZone("UTC+5", 5*60*60)
	sched, err := Parse("30 21 * * *")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	after := time.Date(2026, time.July, 13, 22, 0, 0, 0, loc)
	got := sched.Next(after)
	want := time.Date(2026, time.July, 14, 21, 30, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("Next() = %v, want %v", got, want)
	}
	if got.Location() != loc {
		t.Fatalf("Next() location = %v, want %v", got.Location(), loc)
	}
}

func TestScheduleStringRoundTrips(t *testing.T) {
	spec := "0 19 * * 1-5"
	sched, err := Parse(spec)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if sched.String() != spec {
		t.Fatalf("String() = %q, want %q", sched.String(), spec)
	}
}
