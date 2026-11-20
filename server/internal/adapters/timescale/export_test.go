package timescale

// SetCacheLimit changes the size of the series cache (tests).
func SetCacheLimit(s *Store, n int) { s.cacheLimit = n }
