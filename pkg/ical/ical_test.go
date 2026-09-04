package ical

import (
	"os"
	"strings"
	"testing"
	"time"
)

func loadFixture(t *testing.T, name string, loc *time.Location) *Calendar {
	t.Helper()

	file, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatalf("opening fixture: %v", err)
	}
	defer file.Close()

	calendar, err := Parse(file, loc)
	if err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}

	return calendar
}

func findEvent(t *testing.T, calendar *Calendar, uid string) Event {
	t.Helper()

	for _, event := range calendar.Events {
		if event.UID == uid {
			return event
		}
	}

	t.Fatalf("no event with uid %q", uid)
	return Event{}
}

func TestParseBasic(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatalf("loading timezone: %v", err)
	}

	calendar := loadFixture(t, "basic.ics", london)

	if calendar.Name != "Work" {
		t.Errorf("calendar name = %q, want %q", calendar.Name, "Work")
	}

	// The cancelled event must be dropped, leaving four.
	if len(calendar.Events) != 4 {
		var got []string
		for _, e := range calendar.Events {
			got = append(got, e.UID)
		}
		t.Fatalf("parsed %d events (%s), want 4", len(calendar.Events), strings.Join(got, ", "))
	}

	for _, event := range calendar.Events {
		if event.UID == "cancelled@example.com" {
			t.Error("STATUS:CANCELLED event was not dropped")
		}
	}

	t.Run("utc times", func(t *testing.T) {
		event := findEvent(t, calendar, "timed-utc@example.com")
		want := time.Date(2026, 9, 4, 8, 30, 0, 0, time.UTC)

		if !event.Start.Equal(want) {
			t.Errorf("start = %v, want %v", event.Start, want)
		}
		if event.AllDay {
			t.Error("event marked all-day")
		}
		if got := event.Duration(); got != 15*time.Minute {
			t.Errorf("duration = %v, want 15m", got)
		}
	})

	t.Run("folded lines are rejoined", func(t *testing.T) {
		event := findEvent(t, calendar, "folded@example.com")
		want := "A summary long enough that exporters fold it across more than one line " +
			"and a naive parser would truncate it"

		if event.Summary != want {
			t.Errorf("summary = %q,\nwant %q", event.Summary, want)
		}
	})

	t.Run("escaped text is unescaped", func(t *testing.T) {
		event := findEvent(t, calendar, "folded@example.com")
		want := "Room 1, Floor 3; near the stairs"

		if event.Location != want {
			t.Errorf("location = %q, want %q", event.Location, want)
		}
	})

	t.Run("VALARM is ignored", func(t *testing.T) {
		event := findEvent(t, calendar, "folded@example.com")
		// The alarm carries its own DTSTART in 1999; if it leaked through it
		// would have overwritten the event's own start.
		if event.Start.Year() != 2026 {
			t.Errorf("start = %v, VALARM properties leaked into the event", event.Start)
		}
	})

	t.Run("zoned times use their TZID", func(t *testing.T) {
		event := findEvent(t, calendar, "folded@example.com")
		want := time.Date(2026, 9, 4, 14, 0, 0, 0, london)

		if !event.Start.Equal(want) {
			t.Errorf("start = %v, want %v", event.Start, want)
		}
	})

	t.Run("all-day events have an exclusive end", func(t *testing.T) {
		event := findEvent(t, calendar, "allday@example.com")

		if !event.AllDay {
			t.Error("event not marked all-day")
		}
		if got, want := event.End.Day(), 5; got != want {
			t.Errorf("end day = %d, want %d (DTEND is exclusive)", got, want)
		}
	})

	t.Run("floating times use the given location", func(t *testing.T) {
		event := findEvent(t, calendar, "floating@example.com")
		want := time.Date(2026, 9, 4, 12, 0, 0, 0, london)

		if !event.Start.Equal(want) {
			t.Errorf("start = %v, want %v", event.Start, want)
		}
		// DURATION rather than DTEND.
		if got := event.Duration(); got != 90*time.Minute {
			t.Errorf("duration = %v, want 1h30m", got)
		}
	})
}

func TestParseRejectsNonCalendar(t *testing.T) {
	if _, err := Parse(strings.NewReader("hello, world"), time.UTC); err == nil {
		t.Error("expected an error for a non-calendar document")
	}
}

func TestUnfoldHandlesBareLF(t *testing.T) {
	// Not all exporters use CRLF, despite the spec. The leading whitespace on
	// a continuation line is the fold marker and is removed, so a space that
	// should survive is written as two.
	input := "BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:a\nSUMMARY:Split\n  across lines\nDTSTART:20260904T090000Z\nEND:VEVENT\nEND:VCALENDAR\n"

	calendar, err := Parse(strings.NewReader(input), time.UTC)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(calendar.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(calendar.Events))
	}
	if got, want := calendar.Events[0].Summary, "Split across lines"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestUnfoldStripsOnlyTheFoldMarker(t *testing.T) {
	// A tab is equally valid as the fold marker.
	input := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:a\r\nSUMMARY:one\r\n\ttwo\r\nDTSTART:20260904T090000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

	calendar, err := Parse(strings.NewReader(input), time.UTC)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, want := calendar.Events[0].Summary, "onetwo"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestParseContentLine(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantName   string
		wantValue  string
		wantParams map[string]string
	}{
		{
			name:      "plain",
			raw:       "SUMMARY:Hello",
			wantName:  "SUMMARY",
			wantValue: "Hello",
		},
		{
			name:       "with parameter",
			raw:        "DTSTART;VALUE=DATE:20260904",
			wantName:   "DTSTART",
			wantValue:  "20260904",
			wantParams: map[string]string{"VALUE": "DATE"},
		},
		{
			// Splitting on the first colon outright would break here.
			name:       "quoted parameter containing a colon",
			raw:        `DTSTART;TZID="Weird:Zone";VALUE=DATE-TIME:20260904T090000`,
			wantName:   "DTSTART",
			wantValue:  "20260904T090000",
			wantParams: map[string]string{"TZID": "Weird:Zone", "VALUE": "DATE-TIME"},
		},
		{
			name:      "value containing colons",
			raw:       "URL:https://example.com/a:b",
			wantName:  "URL",
			wantValue: "https://example.com/a:b",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			line, ok := parseContentLine(test.raw)
			if !ok {
				t.Fatal("failed to parse")
			}
			if line.name != test.wantName {
				t.Errorf("name = %q, want %q", line.name, test.wantName)
			}
			if line.value != test.wantValue {
				t.Errorf("value = %q, want %q", line.value, test.wantValue)
			}
			for key, want := range test.wantParams {
				if got := line.param(key); got != want {
					t.Errorf("param %s = %q, want %q", key, got, want)
				}
			}
		})
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{in: "PT1H", want: time.Hour, ok: true},
		{in: "PT15M", want: 15 * time.Minute, ok: true},
		{in: "P1DT2H30M", want: 26*time.Hour + 30*time.Minute, ok: true},
		{in: "P2W", want: 14 * 24 * time.Hour, ok: true},
		{in: "-PT30M", want: -30 * time.Minute, ok: true},
		{in: "", ok: false},
		{in: "1H", ok: false},
		{in: "P", ok: false},
	}

	for _, test := range tests {
		got, ok := parseDuration(test.in)
		if ok != test.ok {
			t.Errorf("parseDuration(%q) ok = %v, want %v", test.in, ok, test.ok)
			continue
		}
		if ok && got != test.want {
			t.Errorf("parseDuration(%q) = %v, want %v", test.in, got, test.want)
		}
	}
}

func TestUnescapeText(t *testing.T) {
	tests := []struct{ in, want string }{
		{in: "plain", want: "plain"},
		{in: `a\, b`, want: "a, b"},
		{in: `a\; b`, want: "a; b"},
		{in: `line\nbreak`, want: "line\nbreak"},
		{in: `line\Nbreak`, want: "line\nbreak"},
		{in: `back\\slash`, want: `back\slash`},
		// An unknown escape keeps both characters rather than swallowing one.
		{in: `keep\qboth`, want: `keep\qboth`},
	}

	for _, test := range tests {
		if got := unescapeText(test.in); got != test.want {
			t.Errorf("unescapeText(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}
