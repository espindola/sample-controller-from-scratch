package ratelimit

import "time"

// RateLimiter is an interface that encapsulates limiting how often an
// operation is performed.
//
// Users should call AskTick when they have an operation to perform
// and then block reading from the channel returned by GetChan.
//
// Implementations send a message on the channel when there has been
// at least one request and the implementation specific rate limit is
// satisfied.
//
// Stop the RateLimiter to release resources.
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
// without a call to AskTick.
func AfterOneSecondIdle() RateLimiter {
	ret := &rateLimiterImpl{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	go func() {
		timer := time.NewTimer(1 * time.Second)
		timer.Stop()
		var tick chan struct{}
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
				// Enable sending on the next loop iteration
				tick = ret.tick
			case tick <- struct{}{}:
				// Disable sending on the next loop iteration
				tick = nil
			}
		}
	}()
	return ret
}
