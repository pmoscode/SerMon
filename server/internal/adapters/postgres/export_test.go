package postgres

import "context"

// CountVisibleOrganizations counts organizations visible outside any tenant transaction
// (test-only: proves that the tenant setting does not leak between transactions).
func (db *DB) CountVisibleOrganizations(ctx context.Context) (int, error) {
	var n int
	err := db.pool.QueryRow(ctx, "SELECT count(*) FROM organizations").Scan(&n)
	return n, err
}
