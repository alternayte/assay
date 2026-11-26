package connectors

import (
	"context"
	"errors"
	"fmt"
	gosync "sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// errPace marks a failed rate reservation. A retry of the request cannot
// help.
var errPace = errors.New("connectors: reserve a request slot")

// pace spaces the requests of a connection evenly at its rate. With a
// database it reserves each slot in connection_rates, so that all processes
// together keep the rate: one round trip per request. Without one it spaces
// the requests of this client only.
type pace struct {
	every time.Duration
	q     *Queries
	id    int64

	mu        gosync.Mutex
	next      time.Time
	heldUntil time.Time
}

func newPace(pool *pgxpool.Pool, connectionID int64, every time.Duration) *pace {
	p := &pace{every: every, id: connectionID}
	if pool != nil && connectionID != 0 {
		p.q = New(pool)
	}
	return p
}

// wait returns at the start of the next request slot. A request that waited
// checks the hold of the connection when its slot starts: a slot that a
// Retry-After answer covers is given up, and the request reserves a slot
// after the hold.
func (p *pace) wait(ctx context.Context) error {
	for {
		d, err := p.reserve(ctx)
		if err != nil {
			return err
		}
		if d <= 0 {
			return nil
		}
		if err := sleep(ctx, d); err != nil {
			return err
		}
		held, err := p.held(ctx)
		if err != nil {
			return err
		}
		if held <= 0 {
			return nil
		}
		if err := sleep(ctx, held); err != nil {
			return err
		}
	}
}

// reserve takes the next slot and returns the time until it starts.
func (p *pace) reserve(ctx context.Context) (time.Duration, error) {
	if p.q == nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		now := time.Now()
		at := p.next
		if at.Before(now) {
			at = now
		}
		p.next = at.Add(p.every)
		return time.Until(at), nil
	}
	p.mu.Lock()
	every := p.every
	p.mu.Unlock()
	secs, err := p.q.ReserveSlot(ctx, ReserveSlotParams{ConnectionID: p.id, Every: every.Seconds()})
	if err != nil {
		return 0, paceError(ctx, err)
	}
	return seconds(secs), nil
}

// slow makes the slots at least every apart.
func (p *pace) slow(every time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if every > p.every {
		p.every = every
	}
}

// held returns the time until the hold of the connection ends.
func (p *pace) held(ctx context.Context) (time.Duration, error) {
	if p.q == nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		return time.Until(p.heldUntil), nil
	}
	secs, err := p.q.HeldFor(ctx, p.id)
	if err != nil {
		return 0, paceError(ctx, err)
	}
	return seconds(secs), nil
}

// hold moves the next slot of the connection to d from now, for every
// client of the connection in every process.
func (p *pace) hold(ctx context.Context, d time.Duration) error {
	if p.q == nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		at := time.Now().Add(d)
		if at.After(p.next) {
			p.next = at
		}
		if at.After(p.heldUntil) {
			p.heldUntil = at
		}
		return nil
	}
	if err := p.q.HoldRate(ctx, HoldRateParams{ConnectionID: p.id, Wait: d.Seconds()}); err != nil {
		return paceError(ctx, err)
	}
	return nil
}

// paceError keeps the cancel of the context visible to the caller.
func paceError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%w: %w", errPace, err)
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
