// Package discovery provides etcd-backed service registration and gRPC
// client-side load balancing. When ETCD_ENDPOINTS is unset, the package
// silently degrades into pass-through direct dialing so single-instance
// deployments and local development do not need an etcd cluster.
package discovery

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	envEtcdEndpoints = "ETCD_ENDPOINTS"
	envAdvertiseAddr = "ADVERTISE_ADDR"

	keyPrefix = "services"

	dialTimeout = 5 * time.Second
)

var (
	initOnce sync.Once
	cli      *clientv3.Client
	initErr  error
)

// Endpoints returns the configured etcd endpoint list. An empty slice means
// etcd discovery is disabled and callers should fall back to direct dialing.
func Endpoints() []string {
	raw := strings.TrimSpace(os.Getenv(envEtcdEndpoints))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// IsEnabled reports whether the ETCD_ENDPOINTS environment variable is set.
// It does NOT verify the cluster is reachable; that surfaces lazily on Client().
func IsEnabled() bool {
	return len(Endpoints()) > 0
}

// Client returns the process-wide etcd client. The first successful call
// caches the connection; subsequent calls return the same instance. The
// returned client must NOT be closed by callers — its lifetime is bound
// to the process.
func Client() (*clientv3.Client, error) {
	initOnce.Do(func() {
		eps := Endpoints()
		if len(eps) == 0 {
			initErr = fmt.Errorf("discovery: %s is not set", envEtcdEndpoints)
			return
		}
		cli, initErr = clientv3.New(clientv3.Config{
			Endpoints:   eps,
			DialTimeout: dialTimeout,
		})
	})
	return cli, initErr
}

// serviceKey returns the canonical etcd key prefix watched by the resolver
// for a given logical service name, e.g. "services/embedding".
func serviceKey(serviceName string) string {
	return keyPrefix + "/" + serviceName
}

// instanceKey returns the per-instance etcd key, e.g.
// "services/embedding/embedding-service-xyz:50051".
func instanceKey(serviceName, advertiseAddr string) string {
	return serviceKey(serviceName) + "/" + advertiseAddr
}
