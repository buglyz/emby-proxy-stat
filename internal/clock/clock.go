// Package clock 提供业务时区（Asia/Shanghai）下的统一时间来源，
// 避免统计口径因宿主机时区不同而漂移。
package clock

import "time"

var businessLocation = func() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	return location
}()

// Now 返回业务时区下的当前时间。
func Now() time.Time { return time.Now().In(businessLocation) }

// Today 返回业务时区下的当天日期字符串（YYYY-MM-DD）。
func Today() string { return Now().Format("2006-01-02") }

// ReportDue 判断 now 是否已到达每日播报时刻 configured（HH:MM，空串按 23:59）。
func ReportDue(now time.Time, configured string) bool {
	if configured == "" {
		configured = "23:59"
	}
	target, err := time.ParseInLocation("15:04", configured, businessLocation)
	if err != nil {
		return false
	}
	targetToday := time.Date(now.Year(), now.Month(), now.Day(), target.Hour(), target.Minute(), 0, 0, businessLocation)
	return !now.Before(targetToday)
}
