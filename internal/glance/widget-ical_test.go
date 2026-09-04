package glance

import (
	"testing"
	"time"
)

func TestNormalizeCalendarURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			// The scheme calendar apps put behind their "subscribe" links.
			name: "webcal becomes https",
			in:   "webcal://p12-caldav.icloud.com/published/2/abc123",
			want: "https://p12-caldav.icloud.com/published/2/abc123",
		},
		{
			name: "webcals becomes https",
			in:   "webcals://example.com/cal.ics",
			want: "https://example.com/cal.ics",
		},
		{
			name: "scheme match is case insensitive",
			in:   "WebCal://example.com/cal.ics",
			want: "https://example.com/cal.ics",
		},
		{
			name: "query strings survive",
			in:   "webcal://example.com/remote.php/dav?export=1&token=abc",
			want: "https://example.com/remote.php/dav?export=1&token=abc",
		},
		{
			name: "https is left alone",
			in:   "https://example.com/cal.ics",
			want: "https://example.com/cal.ics",
		},
		{
			name: "http is left alone",
			in:   "http://localhost:8123/live.ics",
			want: "http://localhost:8123/live.ics",
		},
		{
			// Must not be mistaken for a scheme prefix.
			name: "a path containing webcal is untouched",
			in:   "https://example.com/webcal://thing.ics",
			want: "https://example.com/webcal://thing.ics",
		},
		{
			name: "shorter than the scheme",
			in:   "webcal",
			want: "webcal",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeCalendarURL(test.in); got != test.want {
				t.Errorf("normalizeCalendarURL(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestICalWidgetNormalizesURLsOnInitialize(t *testing.T) {
	widget := &icalWidget{
		Calendars: []icalCalendar{
			{URL: "webcal://example.com/a.ics"},
			{URL: "https://example.com/b.ics"},
		},
	}

	if err := widget.initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	if got, want := widget.Calendars[0].URL, "https://example.com/a.ics"; got != want {
		t.Errorf("calendar 0 url = %q, want %q", got, want)
	}
	if got, want := widget.Calendars[1].URL, "https://example.com/b.ics"; got != want {
		t.Errorf("calendar 1 url = %q, want %q", got, want)
	}
}

func TestRelativeDayLabel(t *testing.T) {
	// A zone with a large offset, where treating a day as a fixed 24 hours of
	// absolute time lands mid-afternoon rather than at midnight.
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatalf("loading timezone: %v", err)
	}

	today := time.Date(2026, 9, 4, 0, 0, 0, 0, kolkata)

	tests := []struct {
		name string
		at   time.Time
		want string
	}{
		{name: "early today", at: time.Date(2026, 9, 4, 0, 30, 0, 0, kolkata), want: "Today"},
		{name: "late today", at: time.Date(2026, 9, 4, 23, 30, 0, 0, kolkata), want: "Today"},
		{name: "tomorrow", at: time.Date(2026, 9, 5, 9, 0, 0, 0, kolkata), want: "Tomorrow"},
		{name: "within the week", at: time.Date(2026, 9, 8, 9, 0, 0, 0, kolkata), want: "Tuesday"},
		{name: "beyond a week", at: time.Date(2026, 9, 20, 9, 0, 0, 0, kolkata), want: "Sun 20 Sep"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := relativeDayLabel(test.at, today); got != test.want {
				t.Errorf("relativeDayLabel = %q, want %q", got, test.want)
			}
		})
	}
}
