package agent

import (
	"context"
	"sync"
	"time"
)

// A Freebuff hosted-model session is paid for when it opens and stays valid for
// about an hour. Starting an orchestrator per task opens a session per task, so
// tasks (and the daemon's own account probes) share one orchestrator per
// account profile and keep it for freebuffIdleTTL after its last user. The
// profile is exclusive to one orchestrator anyway, so concurrent users of an
// account have to share it.

// freebuffIdleTTL is how long an unused orchestrator is kept. It sits just
// under the session's validity so a burst of tasks rides one session and an
// idle host gives the network slot back.
const freebuffIdleTTL = 50 * time.Minute

// FreebuffEnvIdleTTL overrides freebuffIdleTTL with a Go duration ("20m").
// "0" disables sharing: an orchestrator stops when its last user is done.
const FreebuffEnvIdleTTL = "FREEBUFF_IDLE_TTL"

type freebuffStarter func(ctx context.Context, cfg Config, profileDir string) (*FreebuffOrchestrator, error)

type freebuffPoolEntry struct {
	dir   string
	ready chan struct{} // closed once orch/err are set
	orch  *FreebuffOrchestrator
	err   error
	refs  int
	idle  *time.Timer
}

type freebuffPool struct {
	mu      sync.Mutex
	entries map[string]*freebuffPoolEntry
}

var sharedFreebuffPool = &freebuffPool{entries: map[string]*freebuffPoolEntry{}}

func freebuffIdleTTLFor(cfg Config) time.Duration {
	if v := freebuffSetting(cfg, FreebuffEnvIdleTTL); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return freebuffIdleTTL
}

// AcquireFreebuffOrchestrator returns the running orchestrator for profileDir,
// starting it if there is none, and a release func to call when done. The
// orchestrator stays up for the idle TTL after the last release.
func AcquireFreebuffOrchestrator(ctx context.Context, cfg Config, profileDir string) (*FreebuffOrchestrator, func(), error) {
	return sharedFreebuffPool.acquire(ctx, cfg, profileDir, StartFreebuffOrchestrator)
}

// StopFreebuffOrchestrators stops every pooled orchestrator, giving each its
// graceful shutdown so Freebuff releases the session slot. Call it when the
// daemon exits.
func StopFreebuffOrchestrators() { sharedFreebuffPool.stopAll() }

// EvictFreebuffOrchestrator stops the pooled orchestrator for profileDir, for
// when the profile is about to be removed or replaced.
func EvictFreebuffOrchestrator(profileDir string) { sharedFreebuffPool.evict(profileDir) }

func (o *FreebuffOrchestrator) alive() bool {
	select {
	case <-o.done:
		return false
	default:
		return true
	}
}

func (p *freebuffPool) acquire(ctx context.Context, cfg Config, dir string, start freebuffStarter) (*FreebuffOrchestrator, func(), error) {
	ttl := freebuffIdleTTLFor(cfg)
	if ttl == 0 {
		o, err := start(ctx, cfg, dir)
		if err != nil {
			return nil, nil, err
		}
		return o, o.Stop, nil
	}

	for {
		p.mu.Lock()
		e := p.entries[dir]
		if e == nil {
			e = &freebuffPoolEntry{dir: dir, ready: make(chan struct{}), refs: 1}
			p.entries[dir] = e
			p.mu.Unlock()

			// The start is bound to the first caller's ctx only while it waits
			// for readiness; the process outlives it.
			e.orch, e.err = start(ctx, cfg, dir)
			p.mu.Lock()
			if e.err != nil {
				delete(p.entries, dir)
			}
			p.mu.Unlock()
			close(e.ready)
			if e.err != nil {
				return nil, nil, e.err
			}
			return e.orch, p.releaser(e, ttl), nil
		}
		e.refs++
		if e.idle != nil {
			e.idle.Stop()
			e.idle = nil
		}
		p.mu.Unlock()

		select {
		case <-e.ready:
		case <-ctx.Done():
			p.release(e, ttl)
			return nil, nil, ctx.Err()
		}
		if e.err != nil {
			// The starter failed for someone else; try for ourselves.
			p.release(e, ttl)
			continue
		}
		if !e.orch.alive() {
			// It died while idle or mid-use. Drop it and start a fresh one.
			p.dropIfCurrent(e)
			p.release(e, ttl)
			continue
		}
		return e.orch, p.releaser(e, ttl), nil
	}
}

func (p *freebuffPool) releaser(e *freebuffPoolEntry, ttl time.Duration) func() {
	var once sync.Once
	return func() { once.Do(func() { p.release(e, ttl) }) }
}

func (p *freebuffPool) release(e *freebuffPoolEntry, ttl time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e.refs--
	if e.refs > 0 {
		return
	}
	if p.entries[e.dir] != e {
		// Already dropped from the pool: stop it once nobody uses it.
		if e.orch != nil {
			go e.orch.Stop()
		}
		return
	}
	if e.orch == nil || !e.orch.alive() {
		delete(p.entries, e.dir)
		return
	}
	e.idle = time.AfterFunc(ttl, func() {
		p.mu.Lock()
		if e.refs > 0 || p.entries[e.dir] != e {
			p.mu.Unlock()
			return
		}
		delete(p.entries, e.dir)
		p.mu.Unlock()
		e.orch.Stop()
	})
}

func (p *freebuffPool) dropIfCurrent(e *freebuffPoolEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries[e.dir] == e {
		delete(p.entries, e.dir)
	}
}

func (p *freebuffPool) evict(dir string) {
	p.mu.Lock()
	e := p.entries[dir]
	delete(p.entries, dir)
	p.mu.Unlock()
	if e == nil {
		return
	}
	<-e.ready
	if e.orch != nil {
		e.orch.Stop()
	}
}

func (p *freebuffPool) stopAll() {
	p.mu.Lock()
	all := make([]*freebuffPoolEntry, 0, len(p.entries))
	for _, e := range p.entries {
		all = append(all, e)
	}
	p.entries = map[string]*freebuffPoolEntry{}
	p.mu.Unlock()

	var wg sync.WaitGroup
	for _, e := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-e.ready
			if e.orch != nil {
				e.orch.Stop()
			}
		}()
	}
	wg.Wait()
}
