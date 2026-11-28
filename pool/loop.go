package pool

import (
	"context"
	"time"
)

// Renter rents and destroys boxes. The hyperscalr command implements it on
// vast.ai; a dry run logs the decisions and calls nothing.
type Renter interface {
	Rent(ctx context.Context) (Member, error)
	Destroy(ctx context.Context, id string) error
}

// Loop applies the policy every interval: rent when the pool is full, drain
// and destroy the boxes that sat idle. With act false it only logs.
func Loop(ctx context.Context, s *Server, p Policy, r Renter, interval time.Duration, act bool, logf func(string, ...any)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		members := s.ring.Members()
		a := Decide(p, members, s.IdleFor())
		if a.Add {
			logf("pool: %d boxes, all busy: want one more", len(members))
			if act {
				if m, err := r.Rent(ctx); err != nil {
					logf("pool: rent failed: %v", err)
				} else if err := s.ring.Add(m); err != nil {
					logf("pool: add %s: %v", m.ID, err)
				} else {
					logf("pool: rented %s (%s) as %s", m.ID, m.Endpoint, m.ProviderID)
				}
			}
		}
		if a.Drain != "" {
			logf("pool: box %s idle past %s: drain", a.Drain, p.IdleFor)
			if act {
				if err := s.ring.Drain(a.Drain); err != nil {
					logf("pool: drain %s: %v", a.Drain, err)
				} else if err := s.ring.Remove(a.Drain); err == nil {
					if err := r.Destroy(ctx, a.Drain); err != nil {
						logf("pool: destroy %s: %v", a.Drain, err)
					} else {
						logf("pool: destroyed %s", a.Drain)
					}
				}
			}
		}
	}
}
