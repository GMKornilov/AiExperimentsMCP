package app

import (
	"strings"
	"testing"
	"time"
)

func TestNextCronUsesSeparateTimezoneOnly(t *testing.T) {
	after := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	for _, expr := range []string{
		"CRON_TZ=UTC 0 9 * * *",
		"TZ=UTC 0 9 * * *",
		"0 9 * * * *",
		"@daily",
	} {
		t.Run(expr, func(t *testing.T) {
			_, err := nextCron(expr, "America/New_York", after)
			if err == nil || !strings.Contains(err.Error(), "five fields") {
				t.Fatalf("nextCron(%q) error = %v; want five-field rejection", expr, err)
			}
		})
	}
	want := time.Date(2026, time.January, 1, 14, 0, 0, 0, time.UTC)
	got, err := nextCron("0 9 * * *", "America/New_York", after)
	if err != nil || !got.Equal(want) {
		t.Fatalf("nextCron with separate timezone = %s, %v; want %s", got, err, want)
	}
}
