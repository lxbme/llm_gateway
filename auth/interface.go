package auth

type Service interface {
	Create(alias string) (token string, err error)
	Get(token string) (valide bool, alias string, err error)
	Delete(token string) error
}
