package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/Autumn-27/artex/traffic"
	actool "github.com/Autumn-27/norma/tool"
)

// TrafficForOwner returns the isolated recording store for one hosted-service
// owner. Each owner gets its own MITM listener, index, CA and blob tree so the
// same target host cannot leak captured evidence across tenants.
func (m *Manager) TrafficForOwner(ownerID string) (*traffic.Traffic, error) {
	if ownerID == "" {
		return nil, errors.New("owner is required")
	}
	upstream := m.GlobalProxy()
	m.ownerTrafficMu.Lock()
	defer m.ownerTrafficMu.Unlock()
	if tr := m.ownerTraffic[ownerID]; tr != nil {
		return tr, nil
	}

	sum := sha256.Sum256([]byte(ownerID))
	dir := filepath.Join(m.dir, "traffic", "owners", hex.EncodeToString(sum[:16]))
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		addr, err := freeLoopbackAddr()
		if err != nil {
			return nil, err
		}
		tr, err := traffic.Open(dir, addr)
		if err != nil {
			return nil, err
		}
		if err := tr.SetUpstreamProxy(upstream); err != nil {
			_ = tr.Close()
			return nil, err
		}
		go func() {
			if err := tr.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("[traffic] owner proxy stopped: %v", err)
			}
		}()
		if err := waitProxy(addr); err != nil {
			lastErr = err
			_ = tr.Close()
			continue
		}
		m.ownerTraffic[ownerID] = tr
		return tr, nil
	}
	return nil, fmt.Errorf("start owner traffic proxy: %w", lastErr)
}

func freeLoopbackAddr() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", err
	}
	return addr, nil
}

func waitProxy(addr string) error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// ProxyAddrForTask selects the tenant-isolated recorder for hosted tasks and
// preserves the existing process-wide behavior for native tasks.
func (m *Manager) ProxyAddrForTask(t *Task) string {
	if t != nil && t.OwnerID != "" {
		tr, err := m.TrafficForOwner(t.OwnerID)
		if err != nil {
			log.Printf("[traffic] owner %s recorder unavailable: %v", t.OwnerID, err)
			return m.GlobalProxy()
		}
		return tr.ProxyAddr()
	}
	return m.ProxyAddr()
}

func (m *Manager) ProxyCACertForTask(t *Task) string {
	if t != nil && t.OwnerID != "" {
		tr, err := m.TrafficForOwner(t.OwnerID)
		if err != nil {
			return ""
		}
		return tr.CACertPath()
	}
	return m.ProxyCACert()
}

// HostToolsForTask keeps traffic tools scoped to the same owner recorder used by
// the task's egress proxy.
func (m *Manager) HostToolsForTask(t *Task) []actool.CoreTool {
	if t != nil && t.OwnerID != "" {
		tr, err := m.TrafficForOwner(t.OwnerID)
		if err != nil {
			return nil
		}
		return tr.Tools()
	}
	return m.HostTools()
}
