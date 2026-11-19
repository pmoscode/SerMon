package fleet

import (
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/metrics"
)

// Forecast needs at least this many points and this time span for a trend.
const (
	minForecastPoints = 6
	minForecastSpan   = time.Hour
)

// FullAt estimates when the available space reaches zero: a least-squares line through the
// (time, available bytes) points. No forecast for too little data, a flat or growing trend, or a
// date beyond the horizon; an already exhausted file system is full now.
func FullAt(points []metrics.Point, now time.Time, horizon time.Duration) (time.Time, bool) {
	if len(points) < minForecastPoints || points[len(points)-1].Time.Sub(points[0].Time) < minForecastSpan {
		return time.Time{}, false
	}
	origin := points[0].Time
	var n, sumX, sumY, sumXX, sumXY float64
	for _, p := range points {
		x := p.Time.Sub(origin).Seconds()
		n++
		sumX += x
		sumY += p.Avg
		sumXX += x * x
		sumXY += x * p.Avg
	}
	denominator := n*sumXX - sumX*sumX
	if denominator == 0 {
		return time.Time{}, false
	}
	slope := (n*sumXY - sumX*sumY) / denominator // bytes per second
	if slope >= 0 {
		return time.Time{}, false
	}
	intercept := (sumY - slope*sumX) / n
	if points[len(points)-1].Avg <= 0 {
		return now, true
	}
	seconds := -intercept / slope
	full := origin.Add(time.Duration(seconds * float64(time.Second)))
	if full.Before(now) {
		full = now
	}
	if full.After(now.Add(horizon)) {
		return time.Time{}, false
	}
	return full, true
}
