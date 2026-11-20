package gpu

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sentnl/inferoute-node/inferoute-client/pkg/logger"
	"go.uber.org/zap"
)

func TestMain(m *testing.M) {
	logger.SetDefaultLogger(&logger.Logger{Logger: zap.NewNop()})
	os.Exit(m.Run())
}

func testMonitor(ttl time.Duration, query func() (*GPUInfo, error)) *Monitor {
	return &Monitor{cacheTTL: ttl, queryFn: query}
}

func TestGetGPUInfoCachesAndCopies(t *testing.T) {
	var n atomic.Int32
	m := testMonitor(time.Second, func() (*GPUInfo, error) {
		n.Add(1)
		return &GPUInfo{ProductName: "x", Utilization: 11, IsBusy: false}, nil
	})

	a, err := m.GetGPUInfo(context.Background())
	if err != nil {
		t.Fatalf("GetGPUInfo(context.Background()) first call: %v", err)
	}
	b, err := m.GetGPUInfo(context.Background())
	if err != nil {
		t.Fatalf("GetGPUInfo(context.Background()) cached call: %v", err)
	}
	if got := n.Load(); got != 1 {
		t.Errorf("queries = %d, want 1", got)
	}
	if a.ProductName != "x" || b.ProductName != "x" {
		t.Errorf("ProductName a=%q b=%q, want x", a.ProductName, b.ProductName)
	}

	a.IsBusy = true
	c, err := m.GetGPUInfo(context.Background())
	if err != nil {
		t.Fatalf("GetGPUInfo(context.Background()) after mutation: %v", err)
	}
	if c.IsBusy {
		t.Fatal("caller mutation of IsBusy leaked into cache")
	}
}

func TestGetGPUInfoCacheExpires(t *testing.T) {
	var n atomic.Int32
	m := testMonitor(20*time.Millisecond, func() (*GPUInfo, error) {
		n.Add(1)
		return &GPUInfo{ProductName: "x"}, nil
	})

	if _, err := m.GetGPUInfo(context.Background()); err != nil {
		t.Fatalf("GetGPUInfo(context.Background()) first call: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if _, err := m.GetGPUInfo(context.Background()); err != nil {
		t.Fatalf("GetGPUInfo(context.Background()) after expiry: %v", err)
	}
	if got := n.Load(); got != 2 {
		t.Errorf("queries = %d, want 2 after TTL expiry", got)
	}
}

func TestGetGPUInfoCachesError(t *testing.T) {
	var n atomic.Int32
	queryErr := errors.New("boom")
	m := testMonitor(time.Second, func() (*GPUInfo, error) {
		n.Add(1)
		return nil, queryErr
	})

	_, err1 := m.GetGPUInfo(context.Background())
	_, err2 := m.GetGPUInfo(context.Background())
	if err1 == nil || err2 == nil {
		t.Fatalf("GetGPUInfo(context.Background()) errors = %v, %v, want cached error", err1, err2)
	}
	if got := n.Load(); got != 1 {
		t.Errorf("queries = %d, want 1 (error cached)", got)
	}
}

func TestGetGPUInfoSerializesRefresh(t *testing.T) {
	var n atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	m := testMonitor(time.Second, func() (*GPUInfo, error) {
		if n.Add(1) == 1 {
			close(started)
			<-release
		}
		return &GPUInfo{ProductName: "x"}, nil
	})

	const goroutines = 8
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.GetGPUInfo(context.Background()); err != nil {
				errs <- err
			}
		}()
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first query never started")
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("GetGPUInfo(context.Background()) concurrent: %v", err)
	}
	if got := n.Load(); got != 1 {
		t.Errorf("queries = %d, want 1 (mutex serializes refresh)", got)
	}
}
