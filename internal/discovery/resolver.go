package discovery

import (
	"fmt"
	"sync"

	etcdresolver "go.etcd.io/etcd/client/v3/naming/resolver"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/resolver"
)

const roundRobinServiceConfig = `{"loadBalancingConfig":[{"round_robin":{}}]}`

var (
	builderOnce sync.Once
	builderErr  error
)

// ensureBuilder lazily registers the etcd gRPC resolver builder once per
// process. It is safe to call multiple times. When etcd is disabled, the
// builder is never registered.
func ensureBuilder() error {
	builderOnce.Do(func() {
		c, err := Client()
		if err != nil {
			builderErr = err
			return
		}
		b, err := etcdresolver.NewBuilder(c)
		if err != nil {
			builderErr = fmt.Errorf("discovery: build resolver: %w", err)
			return
		}
		resolver.Register(b)
	})
	return builderErr
}

// Dial returns a gRPC ClientConn for the named logical service. When etcd
// is enabled, it dials etcd:///services/<name> with round_robin balancing,
// so the connection automatically tracks the live instance set. When etcd
// is disabled, it falls back to dialing fallbackAddr directly so existing
// *_ADDR env vars keep working.
//
// All connections currently use insecure transport credentials; introducing
// TLS is out of scope for this change.
func Dial(serviceName, fallbackAddr string, extraOpts ...grpc.DialOption) (*grpc.ClientConn, error) {
	opts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}, extraOpts...)

	if !IsEnabled() {
		if fallbackAddr == "" {
			return nil, fmt.Errorf("discovery: %s disabled and no fallback address provided for %s", envEtcdEndpoints, serviceName)
		}
		return grpc.NewClient(fallbackAddr, opts...)
	}

	if err := ensureBuilder(); err != nil {
		return nil, err
	}

	opts = append(opts, grpc.WithDefaultServiceConfig(roundRobinServiceConfig))
	target := "etcd:///" + serviceKey(serviceName)
	return grpc.NewClient(target, opts...)
}
