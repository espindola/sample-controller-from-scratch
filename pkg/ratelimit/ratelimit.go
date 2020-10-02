package ratelimit

import "time"

type RateLimiter interface {
	AskTick()
	GetChan() <-chan struct{}
	Stop()
}

type rateLimiterImpl struct {
	ask  chan struct{}
	tick chan struct{}
	stop chan struct{}
}

func (rl *rateLimiterImpl) AskTick() {
	rl.ask <- struct{}{}
}

func (rl *rateLimiterImpl) GetChan() <-chan struct{} {
	return rl.tick
}

func (rl *rateLimiterImpl) Stop() {
	rl.stop <- struct{}{}
}

// AfterOneSecondIdle returns a RateLimiter that sends a tick after
// the caller is idle for one second. That is, after one second
// without call so AskTick.
func AfterOneSecondIdle() RateLimiter {
	ret := &rateLimiterImpl{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	go func() {
		timer := time.NewTimer(1 * time.Second)
		timer.Stop()
		for {
			select {
			case <-ret.stop:
				return
			case <-ret.ask:
				// We can stop multiple times, so don't use the return from Stop.
				timer.Stop()
				select {
				case <-timer.C:
				default:
				}
				timer.Reset(1 * time.Second)
			case <-timer.C:
				ret.tick <- struct{}{}
			}
		}
	}()
	return ret
}
