package fleet

import (
	"math"
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
	// Centered sums: values of a large array (1e12 bytes) would cancel out in the plain ones.
	origin := points[0].Time
	n := float64(len(points))
	var meanX, meanY float64
	for _, p := range points {
		meanX += p.Time.Sub(origin).Seconds() / n
		meanY += p.Avg / n
	}
	var sxx, sxy float64
	for _, p := range points {
		dx, dy := p.Time.Sub(origin).Seconds()-meanX, p.Avg-meanY
		sxx += dx * dx
		sxy += dx * dy
	}
	if sxx == 0 {
		return time.Time{}, false
	}
	slope := sxy / sxx // bytes per second
	if slope >= 0 {
		return time.Time{}, false
	}
	if points[len(points)-1].Avg <= 0 {
		return now, true
	}
	// Seconds from now until the line reaches zero; compared before converting, as a float
	// beyond the range of a Duration converts differently per platform.
	seconds := meanX - meanY/slope - now.Sub(origin).Seconds()
	if math.IsNaN(seconds) || seconds > horizon.Seconds() {
		return time.Time{}, false
	}
	full := now
	if seconds > 0 {
		full = now.Add(time.Duration(math.Round(seconds)) * time.Second) // a forecast in whole seconds
	}
	return full, true
}
