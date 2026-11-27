package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"gateway/internal/health"
	"gateway/internal/proxy"
	"gateway/pkg/config"
)

type liveRuntime struct {
	config  *config.Config
	router  *proxy.Router
	checker *health.Checker
	ctx     context.Context
	cancel  context.CancelFunc
}

// runtimeManager swaps complete routing and health-check state as one atomic
// snapshot. Existing requests retain the old router until they finish.
type runtimeManager struct {
	ctx       context.Context
	transport http.RoundTripper
	current   atomic.Pointer[liveRuntime]
	reloadMu  sync.Mutex
	checkers  sync.WaitGroup
}

func newRuntimeManager(ctx context.Context, transport http.RoundTripper) *runtimeManager {
	return &runtimeManager{ctx: ctx, transport: transport}
}

func (m *runtimeManager) initialize(cfg *config.Config) error {
	next, err := m.build(cfg)
	if err != nil {
		return err
	}
	m.current.Store(next)
	m.startChecker(next)
	return nil
}

func (m *runtimeManager) reload(path string) error {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()

	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	current := m.current.Load()
	if current == nil {
		return fmt.Errorf("gateway runtime is not initialized")
	}
	if cfg.Listen != current.config.Listen {
		return fmt.Errorf("listen address cannot change during reload (current %q, new %q)",
			current.config.Listen, cfg.Listen)
	}

	next, err := m.build(cfg)
	if err != nil {
		return err
	}
	m.current.Store(next)
	m.startChecker(next)
	current.cancel()
	return nil
}

func (m *runtimeManager) build(cfg *config.Config) (*liveRuntime, error) {
	router, err := proxy.NewRouter(cfg, m.transport)
	if err != nil {
		return nil, fmt.Errorf("router: %w", err)
	}
	checker := &health.Checker{
		Upstreams: router.Upstreams(),
		Path:      cfg.Health.Path,
		Interval:  cfg.Health.Interval,
		Timeout:   cfg.Health.Timeout,
	}
	checker.CheckOnce(m.ctx)
	checkerCtx, cancel := context.WithCancel(m.ctx)
	return &liveRuntime{
		config:  cfg,
		router:  router,
		checker: checker,
		ctx:     checkerCtx,
		cancel:  cancel,
	}, nil
}

func (m *runtimeManager) startChecker(state *liveRuntime) {
	m.checkers.Add(1)
	go func() {
		defer m.checkers.Done()
		state.checker.Run(state.ctx)
	}()
}

func (m *runtimeManager) serveHTTP(w http.ResponseWriter, r *http.Request) {
	current := m.current.Load()
	if current == nil {
		http.Error(w, "gateway starting", http.StatusServiceUnavailable)
		return
	}
	current.router.ServeHTTP(w, r)
}

func (m *runtimeManager) ready() bool {
	current := m.current.Load()
	return current != nil && current.router.Ready()
}

func (m *runtimeManager) router() *proxy.Router {
	current := m.current.Load()
	if current == nil {
		return nil
	}
	return current.router
}

func (m *runtimeManager) config() *config.Config {
	current := m.current.Load()
	if current == nil {
		return nil
	}
	return current.config
}

func (m *runtimeManager) wait() { m.checkers.Wait() }
