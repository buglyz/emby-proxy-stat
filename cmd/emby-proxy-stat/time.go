package main

import "time"

var businessLocation = func() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	return location
}()

func businessNow() time.Time {
	return time.Now().In(businessLocation)
}

func reportDue(now time.Time, configured string) bool {
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
