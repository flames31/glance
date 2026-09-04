package ical

import (
	"testing"
	"time"
)

// day returns a window covering the whole of the given date in loc.
func day(t *testing.T, loc *time.Location, year int, month time.Month, date int) (time.Time, time.Time) {
	t.Helper()

	from := time.Date(year, month, date, 0, 0, 0, 0, loc)
	return from, from.AddDate(0, 0, 1)
}

func formatTimes(times []time.Time) []string {
	out := make([]string, 0, len(times))
	for _, t := range times {
		out = append(out, t.Format("2006-01-02 15:04"))
	}
	return out
}

func assertOccurrences(t *testing.T, event Event, from, to time.Time, want []string) {
	t.Helper()

	got := formatTimes(event.Occurrences(from, to))
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("occurrence %d = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestWeeklyByDayOccurrences(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatalf("loading timezone: %v", err)
	}

	calendar := loadFixture(t, "recurring.ics", london)
	event := findEvent(t, calendar, "weekly@example.com")

	// The series is every Monday and Wednesday from Wed 2 September 2026,
	// with Wednesday 9 September excluded via EXDATE.
	tests := []struct {
		name string
		date int
		want []string
	}{
		{name: "the first occurrence, on DTSTART", date: 2, want: []string{"2026-09-02 09:00"}},
		{name: "a Tuesday has none", date: 8, want: nil},
		{name: "the EXDATE Wednesday is skipped", date: 9, want: nil},
		{name: "the following Monday still occurs", date: 14, want: []string{"2026-09-14 09:00"}},
		{name: "a Monday weeks later still occurs", date: 28, want: []string{"2026-09-28 09:00"}},
		{
			// The rule cannot produce anything before DTSTART, even though
			// Monday 31 August is in the same week.
			name: "nothing before DTSTART",
			date: 31,
			want: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			month := time.September
			if test.date == 31 {
				month = time.August
			}
			from, to := day(t, london, 2026, month, test.date)
			assertOccurrences(t, event, from, to, test.want)
		})
	}
}

func TestCountTerminatesSeries(t *testing.T) {
	calendar := loadFixture(t, "recurring.ics", time.UTC)
	event := findEvent(t, calendar, "daily-count@example.com")

	// FREQ=DAILY;COUNT=3 from 1 September, so the 1st, 2nd and 3rd only.
	for _, date := range []int{1, 2, 3} {
		from, to := day(t, time.UTC, 2026, time.September, date)
		if got := event.Occurrences(from, to); len(got) != 1 {
			t.Errorf("September %d: got %v, want one occurrence", date, formatTimes(got))
		}
	}

	from, to := day(t, time.UTC, 2026, time.September, 4)
	if got := event.Occurrences(from, to); len(got) != 0 {
		t.Errorf("September 4: got %v, want none (COUNT=3 exhausted)", formatTimes(got))
	}
}

func TestUnsupportedRuleYieldsOnlyTheOriginalEvent(t *testing.T) {
	calendar := loadFixture(t, "recurring.ics", time.UTC)
	event := findEvent(t, calendar, "unsupported@example.com")

	if event.Recurrence == nil {
		t.Fatal("no recurrence rule parsed")
	}
	if event.Recurrence.Supported {
		t.Error("FREQ=MONTHLY;BYDAY=-1FR was treated as supported")
	}

	// It still shows on its own date...
	from, to := day(t, time.UTC, 2026, time.September, 25)
	assertOccurrences(t, event, from, to, []string{"2026-09-25 17:00"})

	// ...but we do not guess at later ones.
	from, to = day(t, time.UTC, 2026, time.October, 30)
	assertOccurrences(t, event, from, to, nil)
}

func TestUntilTerminatesSeries(t *testing.T) {
	event := Event{
		Start: time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC),
		Recurrence: &RecurrenceRule{
			Freq:      "DAILY",
			Interval:  1,
			Until:     time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC),
			HasUntil:  true,
			Supported: true,
		},
	}

	from, to := day(t, time.UTC, 2026, time.September, 3)
	assertOccurrences(t, event, from, to, []string{"2026-09-03 09:00"})

	from, to = day(t, time.UTC, 2026, time.September, 4)
	assertOccurrences(t, event, from, to, nil)
}

func TestIntervalIsRespected(t *testing.T) {
	event := Event{
		Start:      time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC),
		End:        time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC),
		Recurrence: &RecurrenceRule{Freq: "DAILY", Interval: 3, Supported: true},
	}

	// Every third day: the 1st, 4th, 7th...
	for date, want := range map[int]int{1: 1, 2: 0, 3: 0, 4: 1, 7: 1, 8: 0} {
		from, to := day(t, time.UTC, 2026, time.September, date)
		if got := len(event.Occurrences(from, to)); got != want {
			t.Errorf("September %d: got %d occurrences, want %d", date, got, want)
		}
	}
}

func TestDistantSeriesIsFoundWithoutWalkingEveryPeriod(t *testing.T) {
	// A daily series started years ago must still resolve quickly and land on
	// the right day — this is what the skip-ahead in periodsBefore is for.
	event := Event{
		Start:      time.Date(2015, 1, 1, 9, 0, 0, 0, time.UTC),
		End:        time.Date(2015, 1, 1, 9, 30, 0, 0, time.UTC),
		Recurrence: &RecurrenceRule{Freq: "DAILY", Interval: 1, Supported: true},
	}

	from, to := day(t, time.UTC, 2026, time.September, 4)
	assertOccurrences(t, event, from, to, []string{"2026-09-04 09:00"})
}

func TestMonthlySkipsMonthsWithoutTheDate(t *testing.T) {
	// The 31st does not exist in September; RFC 5545 skips such dates rather
	// than rolling over into the next month.
	event := Event{
		Start:      time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC),
		End:        time.Date(2026, 8, 31, 9, 30, 0, 0, time.UTC),
		Recurrence: &RecurrenceRule{Freq: "MONTHLY", Interval: 1, Supported: true},
	}

	for _, date := range []int{1, 2, 30} {
		from, to := day(t, time.UTC, 2026, time.September, date)
		if got := event.Occurrences(from, to); len(got) != 0 {
			t.Errorf("September %d: got %v, want none", date, formatTimes(got))
		}
	}

	from, to := day(t, time.UTC, 2026, time.October, 31)
	assertOccurrences(t, event, from, to, []string{"2026-10-31 09:00"})
}

func TestRecurringEventKeepsWallClockTimeAcrossDST(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatalf("loading timezone: %v", err)
	}

	// British Summer Time ends on 25 October 2026. A 09:00 daily meeting must
	// still be at 09:00 local afterwards, not 08:00 or 10:00.
	event := Event{
		Start:      time.Date(2026, 10, 20, 9, 0, 0, 0, london),
		End:        time.Date(2026, 10, 20, 9, 30, 0, 0, london),
		Recurrence: &RecurrenceRule{Freq: "DAILY", Interval: 1, Supported: true},
	}

	from, to := day(t, london, 2026, time.October, 28)
	got := event.Occurrences(from, to)
	if len(got) != 1 {
		t.Fatalf("got %v, want one occurrence", formatTimes(got))
	}
	if hour, minute, _ := got[0].Clock(); hour != 9 || minute != 0 {
		t.Errorf("occurrence at %02d:%02d, want 09:00 local", hour, minute)
	}
}

func TestOccurrenceInProgressIsIncluded(t *testing.T) {
	// An all-day event, and a long meeting that started before the window
	// opened, are both still happening during it.
	allDay := Event{
		Start:  time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC),
		End:    time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC),
		AllDay: true,
	}

	from := time.Date(2026, 9, 4, 14, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 4, 15, 0, 0, 0, time.UTC)

	if got := allDay.Occurrences(from, to); len(got) != 1 {
		t.Errorf("all-day event: got %v, want one occurrence", formatTimes(got))
	}

	overrunning := Event{
		Start: time.Date(2026, 9, 4, 9, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 9, 4, 17, 0, 0, 0, time.UTC),
	}

	if got := overrunning.Occurrences(from, to); len(got) != 1 {
		t.Errorf("in-progress event: got %v, want one occurrence", formatTimes(got))
	}
}

func TestParseRecurrenceRuleMarksUnsupportedParts(t *testing.T) {
	tests := []struct {
		rule      string
		supported bool
	}{
		{rule: "FREQ=DAILY", supported: true},
		{rule: "FREQ=WEEKLY;BYDAY=MO,WE,FR", supported: true},
		{rule: "FREQ=WEEKLY;INTERVAL=2;COUNT=10", supported: true},
		{rule: "FREQ=MONTHLY;INTERVAL=3", supported: true},
		{rule: "FREQ=YEARLY", supported: true},
		// Ordinal weekdays select "the second Monday", which we do not model.
		{rule: "FREQ=MONTHLY;BYDAY=2MO", supported: false},
		{rule: "FREQ=MONTHLY;BYSETPOS=-1;BYDAY=FR", supported: false},
		{rule: "FREQ=YEARLY;BYWEEKNO=20", supported: false},
		{rule: "FREQ=HOURLY", supported: false},
		// BYDAY under a non-weekly frequency means something different.
		{rule: "FREQ=MONTHLY;BYDAY=MO", supported: false},
	}

	for _, test := range tests {
		rule := parseRecurrenceRule(test.rule, time.UTC)
		if rule.Supported != test.supported {
			t.Errorf("%q supported = %v, want %v", test.rule, rule.Supported, test.supported)
		}
	}
}
