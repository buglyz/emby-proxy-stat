package clock

import (
	"testing"
	"time"
)

func TestReportDue(t *testing.T) {
	before := time.Date(2026, 9, 7, 23, 58, 0, 0, businessLocation)
	after := time.Date(2026, 9, 7, 23, 59, 1, 0, businessLocation)
	afterAltZone := time.Date(2026, 9, 7, 15, 59, 1, 0, time.UTC) // = 北京时间 23:59:01

	if ReportDue(before, "23:59") {
		t.Fatal("report should not be due before configured time")
	}
	if !ReportDue(after, "23:59") {
		t.Fatal("report should be due after configured time")
	}
	if !ReportDue(afterAltZone, "23:59") {
		t.Fatal("report due time must be evaluated in business timezone")
	}
	if !ReportDue(after, "") {
		t.Fatal("empty config should default to 23:59")
	}
	if ReportDue(before, "bad-format") {
		t.Fatal("invalid format must never be due")
	}
}
