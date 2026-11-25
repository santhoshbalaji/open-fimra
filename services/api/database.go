package main

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// openPool connects with PostgreSQL's just-in-time compilation turned off.
// PostGIS functions carry high cost estimates, so the planner compiles even
// small spatial queries. On a state's real forest layer this added about
// 1.7 seconds to every ingest batch and made it three times slower.
func openPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.RuntimeParams["jit"] = "off"
	return pgxpool.NewWithConfig(ctx, cfg)
}
