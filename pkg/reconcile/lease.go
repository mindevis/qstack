package reconcile

import (
	"context"
	"fmt"
	"hash/fnv"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mindevis/qstack/pkg/clock"
)

// ShardOf returns the shard of id: fnv-1a(id) mod shards.
func ShardOf(id string, shards int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return int(h.Sum32() % uint32(shards)) //nolint:gosec // shards is a small positive config value
}

// leaser keeps this member's shard leases in controller_leases.
type leaser struct {
	pool       *pgxpool.Pool
	controller string
	member     string
	shards     int
	ttl        time.Duration
	clock      clock.Clock

	mu         sync.RWMutex
	owned      map[int]bool
	validUntil time.Time // local clock; leases are not trusted after it
}

// safetyMargin keeps the local view of a lease shorter than the database's.
const safetyMargin = 500 * time.Millisecond

// owns reports whether shard is held under a lease known to be valid.
func (l *leaser) owns(shard int) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.owned[shard] && l.clock.Now().Before(l.validUntil)
}

func (l *leaser) ownedShards() []int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if !l.clock.Now().Before(l.validUntil) {
		return nil
	}
	out := make([]int, 0, len(l.owned))
	for s := range l.owned {
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// sync heartbeats, renews held leases, releases shards above the fair share
// and acquires expired ones up to it. It returns the shards newly acquired.
// On error the local leases are dropped: nothing is processed until a
// successful sync confirms them again.
func (l *leaser) sync(ctx context.Context) (acquired []int, err error) {
	start := l.clock.Now()
	ttl := l.ttl.Seconds()
	var owned []int
	err = pgx.BeginFunc(ctx, l.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO controller_members (controller, member, expire_at)
			VALUES ($1, $2, now() + make_interval(secs => $3))
			ON CONFLICT (controller, member) DO UPDATE SET expire_at = EXCLUDED.expire_at`,
			l.controller, l.member, ttl); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO controller_leases (controller, shard)
			SELECT $1, g FROM generate_series(0, $2 - 1) AS g
			ON CONFLICT DO NOTHING`, l.controller, l.shards); err != nil {
			return err
		}
		var members int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM controller_members WHERE controller = $1 AND expire_at > now()`,
			l.controller).Scan(&members); err != nil {
			return err
		}
		share := (l.shards + max(members, 1) - 1) / max(members, 1)

		rows, err := tx.Query(ctx, `
			UPDATE controller_leases SET expire_at = now() + make_interval(secs => $4)
			WHERE controller = $1 AND holder = $2 AND shard < $3
			RETURNING shard`, l.controller, l.member, l.shards, ttl)
		if err != nil {
			return err
		}
		if owned, err = pgx.CollectRows(rows, pgx.RowTo[int]); err != nil {
			return err
		}
		slices.Sort(owned)

		if extra := len(owned) - share; extra > 0 {
			release := owned[len(owned)-extra:]
			owned = owned[:len(owned)-extra]
			if _, err := tx.Exec(ctx, `
				UPDATE controller_leases SET holder = '', expire_at = '-infinity'
				WHERE controller = $1 AND holder = $2 AND shard = ANY($3)`, l.controller, l.member, release); err != nil {
				return err
			}
		}
		if want := share - len(owned); want > 0 {
			rows, err := tx.Query(ctx, `
				UPDATE controller_leases SET holder = $2, expire_at = now() + make_interval(secs => $5)
				WHERE controller = $1 AND shard IN (
					SELECT shard FROM controller_leases
					WHERE controller = $1 AND shard < $3 AND expire_at <= now()
					ORDER BY shard LIMIT $4 FOR UPDATE SKIP LOCKED)
				RETURNING shard`, l.controller, l.member, l.shards, want, ttl)
			if err != nil {
				return err
			}
			if acquired, err = pgx.CollectRows(rows, pgx.RowTo[int]); err != nil {
				return err
			}
			owned = append(owned, acquired...)
		}
		return nil
	})

	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		l.owned, l.validUntil = nil, time.Time{}
		return nil, fmt.Errorf("reconcile: leases: %w", err)
	}
	l.owned = make(map[int]bool, len(owned))
	for _, s := range owned {
		l.owned[s] = true
	}
	// The database set expire_at = now() + ttl after start, so start + ttl
	// is never later than the real expiry.
	l.validUntil = start.Add(l.ttl - safetyMargin)
	return acquired, nil
}

// release gives up all leases and membership (graceful stop), so other
// replicas take the shards without waiting for the TTL.
func (l *leaser) release(ctx context.Context) error {
	l.mu.Lock()
	l.owned, l.validUntil = nil, time.Time{}
	l.mu.Unlock()
	return pgx.BeginFunc(ctx, l.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE controller_leases SET holder = '', expire_at = '-infinity' WHERE controller = $1 AND holder = $2`,
			l.controller, l.member); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM controller_members WHERE controller = $1 AND (member = $2 OR expire_at < now() - interval '1 hour')`,
			l.controller, l.member)
		return err
	})
}
