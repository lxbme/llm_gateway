package redis

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

const (
	tokenPrefix   = "sk"
	entropyLength = 32
	keyPrefix     = "auth:token:"
)

type RedisAuthService struct {
	rdbClient *redis.Client
}

func NewRedisAuthService(addr, password string, db int) (*RedisAuthService, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis auth: failed to connect: %w", err)
	}
	return &RedisAuthService{rdbClient: client}, nil
}

// Create generates a new sk-xxx token for the given alias and stores it in Redis.
func (s *RedisAuthService) Create(alias string) (token string, err error) {
	ctx := context.Background()
	tokenString, err := GenerateToken(tokenPrefix, entropyLength)
	if err != nil {
		return "", fmt.Errorf("fail to create token: %w", err)
	}

	// SET keyPrefix+token alias
	if err := s.rdbClient.Set(ctx, keyPrefix+tokenString, alias, 0).Err(); err != nil {
		return "", fmt.Errorf("fail to store token: %w", err)
	}
	return tokenString, nil
}

// Get validates the token format and looks up the alias in Redis.
func (s *RedisAuthService) Get(token string) (valid bool, alias string, err error) {
	ctx := context.Background()
	if !CheckTokenFormat(tokenPrefix, entropyLength, token) {
		return false, "", nil
	}

	// GET keyPrefix+token
	alias, err = s.rdbClient.Get(ctx, keyPrefix+token).Result()
	if err != nil {
		if err == redis.Nil {
			return false, "", nil
		}
		return false, "", fmt.Errorf("fail to get token: %w", err)
	}
	return true, alias, nil
}

// Delete removes the token from Redis.
func (s *RedisAuthService) Delete(token string) error {
	ctx := context.Background()
	if err := s.rdbClient.Del(ctx, keyPrefix+token).Err(); err != nil {
		return fmt.Errorf("fail to delete token: %w", err)
	}
	return nil
}
