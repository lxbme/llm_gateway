package auth

import "context"

type Service interface {
	Create(ctx context.Context, alias string) (token string, err error)
	Get(ctx context.Context, token string) (valide bool, alias string, err error)
	Delete(ctx context.Context, token string) error
}
