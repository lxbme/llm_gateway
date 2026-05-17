package discovery

import (
	"context"
	"fmt"
	"log"
	"os"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/naming/endpoints"
)

// leaseTTLSeconds is the per-instance lease TTL. If the keepalive loop dies
// (process killed -9, network partition), etcd will evict the key after this
// many seconds.
const leaseTTLSeconds = 10

// AdvertiseAddr returns the address this process should register with etcd.
// Precedence: ADVERTISE_ADDR env var, else hostname:fallbackPort.
func AdvertiseAddr(fallbackPort string) (string, error) {
	if v := os.Getenv(envAdvertiseAddr); v != "" {
		return v, nil
	}
	host, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("discovery: resolve hostname: %w", err)
	}
	return host + ":" + fallbackPort, nil
}

// Register registers (serviceName, advertiseAddr) under etcd with a lease and
// starts a background keepalive loop. The returned deregister function should
// be called during graceful shutdown to actively remove the key (rather than
// waiting for the lease to expire).
//
// When etcd is disabled (ETCD_ENDPOINTS unset), Register is a no-op: it logs
// the fact and returns a deregister that does nothing.
func Register(ctx context.Context, serviceName, advertiseAddr string) (func(), error) {
	if !IsEnabled() {
		log.Printf("[Info] discovery: ETCD_ENDPOINTS not set, skipping registration for %s", serviceName)
		return func() {}, nil
	}
	if advertiseAddr == "" {
		return nil, fmt.Errorf("discovery: advertiseAddr is empty")
	}

	c, err := Client()
	if err != nil {
		return nil, fmt.Errorf("discovery: etcd client: %w", err)
	}

	leaseCtx, leaseCancel := context.WithCancel(ctx)
	lease, err := c.Grant(leaseCtx, leaseTTLSeconds)
	if err != nil {
		leaseCancel()
		return nil, fmt.Errorf("discovery: grant lease: %w", err)
	}

	mgr, err := endpoints.NewManager(c, serviceKey(serviceName))
	if err != nil {
		leaseCancel()
		return nil, fmt.Errorf("discovery: new endpoints manager: %w", err)
	}

	key := instanceKey(serviceName, advertiseAddr)
	endpoint := endpoints.Endpoint{Addr: advertiseAddr}
	if err := mgr.AddEndpoint(leaseCtx, key, endpoint, clientv3.WithLease(lease.ID)); err != nil {
		leaseCancel()
		return nil, fmt.Errorf("discovery: add endpoint: %w", err)
	}

	keepAliveCh, err := c.KeepAlive(leaseCtx, lease.ID)
	if err != nil {
		leaseCancel()
		return nil, fmt.Errorf("discovery: keepalive: %w", err)
	}

	go func() {
		for {
			select {
			case <-leaseCtx.Done():
				return
			case _, ok := <-keepAliveCh:
				if !ok {
					log.Printf("[Error] discovery: keepalive channel closed for %s", key)
					return
				}
			}
		}
	}()

	log.Printf("[Info] discovery: registered %s -> %s (lease=%d)", key, advertiseAddr, lease.ID)

	deregister := func() {
		// Use a fresh background context so deregister still works after the
		// parent ctx is cancelled during shutdown.
		bg := context.Background()
		if err := mgr.DeleteEndpoint(bg, key); err != nil {
			log.Printf("[Error] discovery: delete endpoint %s: %v", key, err)
		}
		if _, err := c.Revoke(bg, lease.ID); err != nil {
			log.Printf("[Error] discovery: revoke lease %d: %v", lease.ID, err)
		}
		leaseCancel()
		log.Printf("[Info] discovery: deregistered %s", key)
	}

	return deregister, nil
}
