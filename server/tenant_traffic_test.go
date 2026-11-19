package server

import (
	"testing"

	"github.com/Autumn-27/artex/traffic"
)

func TestTenantTrafficUsesDistinctListenersAndStores(t *testing.T) {
	m := &Manager{dir: t.TempDir(), ownerTraffic: map[string]*traffic.Traffic{}}
	t.Cleanup(func() {
		m.ownerTrafficMu.Lock()
		defer m.ownerTrafficMu.Unlock()
		for _, store := range m.ownerTraffic {
			_ = store.Close()
		}
	})
	a, err := m.TrafficForOwner("owner-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.TrafficForOwner("owner-b")
	if err != nil {
		t.Fatal(err)
	}
	if a == b || a.ProxyAddr() == b.ProxyAddr() {
		t.Fatalf("tenant traffic stores are shared: %p/%p %s/%s", a, b, a.ProxyAddr(), b.ProxyAddr())
	}
	if a.CACertPath() == b.CACertPath() {
		t.Fatalf("tenant CA paths collide: %s", a.CACertPath())
	}
}
