package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeStarts struct {
	started atomic.Int32
	stopped atomic.Int32
	mu      sync.Mutex
	last    *FreebuffOrchestrator
	fail    error
	delay   time.Duration
}

func (f *fakeStarts) start(ctx context.Context, cfg Config, dir string) (*FreebuffOrchestrator, error) {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.fail != nil {
		return nil, f.fail
	}
	f.started.Add(1)
	done := make(chan struct{})
	o := &FreebuffOrchestrator{done: done}
	var once sync.Once
	o.cancel = func() {
		f.stopped.Add(1)
		once.Do(func() { close(done) })
	}
	f.mu.Lock()
	f.last = o
	f.mu.Unlock()
	return o, nil
}

func ttlCfg(v string) Config { return Config{Env: map[string]string{FreebuffEnvIdleTTL: v}} }

func newTestPool() *freebuffPool { return &freebuffPool{entries: map[string]*freebuffPoolEntry{}} }

func TestFreebuffPoolReusesOrchestratorWithinTTL(t *testing.T) {
	p, f := newTestPool(), &fakeStarts{}
	ctx := context.Background()
	o1, rel1, err := p.acquire(ctx, ttlCfg("1h"), "d", f.start)
	if err != nil {
		t.Fatal(err)
	}
	rel1()
	o2, rel2, err := p.acquire(ctx, ttlCfg("1h"), "d", f.start)
	if err != nil {
		t.Fatal(err)
	}
	defer rel2()
	if o1 != o2 || f.started.Load() != 1 || f.stopped.Load() != 0 {
		t.Fatalf("want one shared live orchestrator, started=%d stopped=%d same=%v", f.started.Load(), f.stopped.Load(), o1 == o2)
	}
}

func TestFreebuffPoolStartsOnceForConcurrentCallers(t *testing.T) {
	p, f := newTestPool(), &fakeStarts{delay: 50 * time.Millisecond}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, rel, err := p.acquire(context.Background(), ttlCfg("1h"), "d", f.start)
			if err != nil {
				t.Error(err)
				return
			}
			rel()
		}()
	}
	wg.Wait()
	if n := f.started.Load(); n != 1 {
		t.Fatalf("started %d orchestrators for one profile", n)
	}
}

func TestFreebuffPoolStopsAfterIdleTTL(t *testing.T) {
	p, f := newTestPool(), &fakeStarts{}
	_, rel, err := p.acquire(context.Background(), ttlCfg("30ms"), "d", f.start)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	rel() // releasing twice must not double count
	deadline := time.Now().Add(2 * time.Second)
	for f.stopped.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.stopped.Load() != 1 {
		t.Fatalf("idle orchestrator not stopped once: %d", f.stopped.Load())
	}
	if _, rel, err = p.acquire(context.Background(), ttlCfg("30ms"), "d", f.start); err != nil {
		t.Fatal(err)
	}
	rel()
	if f.started.Load() != 2 {
		t.Fatalf("want a fresh start after the idle stop, started=%d", f.started.Load())
	}
}

func TestFreebuffPoolReplacesADeadOrchestrator(t *testing.T) {
	p, f := newTestPool(), &fakeStarts{}
	o1, rel, _ := p.acquire(context.Background(), ttlCfg("1h"), "d", f.start)
	rel()
	o1.cancel() // the process exited while idle
	o2, rel2, err := p.acquire(context.Background(), ttlCfg("1h"), "d", f.start)
	if err != nil {
		t.Fatal(err)
	}
	defer rel2()
	if o2 == o1 || f.started.Load() != 2 {
		t.Fatalf("dead orchestrator was handed out again (started=%d)", f.started.Load())
	}
}

func TestFreebuffPoolZeroTTLStopsOnRelease(t *testing.T) {
	p, f := newTestPool(), &fakeStarts{}
	_, rel, err := p.acquire(context.Background(), ttlCfg("0"), "d", f.start)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	if f.stopped.Load() != 1 {
		t.Fatalf("ttl 0 should stop on release, stopped=%d", f.stopped.Load())
	}
}

func TestFreebuffPoolDoesNotCacheAStartFailure(t *testing.T) {
	p, f := newTestPool(), &fakeStarts{fail: errors.New("boom")}
	if _, _, err := p.acquire(context.Background(), ttlCfg("1h"), "d", f.start); err == nil {
		t.Fatal("want the start error")
	}
	f.fail = nil
	_, rel, err := p.acquire(context.Background(), ttlCfg("1h"), "d", f.start)
	if err != nil {
		t.Fatalf("a failed start must not poison the profile: %v", err)
	}
	rel()
}

func TestFreebuffPoolStopAllAndEvict(t *testing.T) {
	p, f := newTestPool(), &fakeStarts{}
	_, relA, _ := p.acquire(context.Background(), ttlCfg("1h"), "a", f.start)
	_, relB, _ := p.acquire(context.Background(), ttlCfg("1h"), "b", f.start)
	relA()
	relB()
	p.evict("a")
	if f.stopped.Load() != 1 {
		t.Fatalf("evict should stop only a: %d", f.stopped.Load())
	}
	p.stopAll()
	if f.stopped.Load() != 2 {
		t.Fatalf("stopAll should stop the rest: %d", f.stopped.Load())
	}
}
