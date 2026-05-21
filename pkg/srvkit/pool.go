package srvkit

import (
	"database/sql"
	"time"
)

// Connection-pool defaults applied by TunePool. Sized against the
// ops/sli-slo.md 120-connection Postgres alert: with six services each
// capped at 20 open connections, the steady-state ceiling is 120, leaving
// the alert as a genuine over-budget signal rather than normal operation.
const (
	// poolMaxOpenConns caps total open connections per service. 6 services x
	// 20 = 120, the documented Postgres connection budget.
	poolMaxOpenConns = 20
	// poolMaxIdleConns keeps a small warm pool so bursty traffic doesn't pay
	// connection-setup latency, without pinning the full open budget idle.
	poolMaxIdleConns = 5
	// poolConnMaxLifetime recycles connections periodically so load
	// rebalances after a Postgres failover / proxy restart and long-lived
	// server-side state can't accumulate.
	poolConnMaxLifetime = 30 * time.Minute
	// poolConnMaxIdleTime returns idle connections to the server so a quiet
	// service doesn't hold the budget hostage from busier peers.
	poolConnMaxIdleTime = 5 * time.Minute
)

// TunePool applies the shared connection-pool limits to db. Safe to call
// immediately after sql.Open (before or after Ping); the SetMax*/SetConn*
// setters only configure the pool and never open a connection themselves.
func TunePool(db *sql.DB) {
	if db == nil {
		return
	}
	db.SetMaxOpenConns(poolMaxOpenConns)
	db.SetMaxIdleConns(poolMaxIdleConns)
	db.SetConnMaxLifetime(poolConnMaxLifetime)
	db.SetConnMaxIdleTime(poolConnMaxIdleTime)
}
