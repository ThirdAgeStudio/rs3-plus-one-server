package limit

import (
	"sync"
	"time"
)

const maxKeys = 10_000

type Limiter struct {
	burst int
	every time.Duration
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	allowance int
	refilled  time.Time
}

func New(burst int, every time.Duration, now func() time.Time) *Limiter {
	return &Limiter{burst: burst, every: every, now: now, buckets: map[string]*bucket{}}
}

func (limiter *Limiter) Take(key string) time.Duration {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now()
	found, ok := limiter.buckets[key]
	if !ok {
		if len(limiter.buckets) >= maxKeys {
			limiter.forgetFull(now)
		}
		found = &bucket{allowance: limiter.burst, refilled: now}
		limiter.buckets[key] = found
	}
	limiter.refill(found, now)
	if found.allowance == 0 {
		return found.refilled.Add(limiter.every).Sub(now)
	}
	found.allowance--
	return 0
}

func (limiter *Limiter) refill(found *bucket, now time.Time) {
	gained := int(now.Sub(found.refilled) / limiter.every)
	if gained > 0 {
		found.allowance = min(limiter.burst, found.allowance+gained)
		found.refilled = found.refilled.Add(time.Duration(gained) * limiter.every)
	}
}

func (limiter *Limiter) forgetFull(now time.Time) {
	for key, found := range limiter.buckets {
		limiter.refill(found, now)
		if found.allowance == limiter.burst {
			delete(limiter.buckets, key)
		}
	}
}
