package embedding

import "context"

// Service defines the interface for embedding operations
type Service interface {
	Get(ctx context.Context, question string) (tensor []float32, err error)
	Info(ctx context.Context) (Info, error)
}

type Info struct {
	Provider   string
	Model      string
	Dimensions int
}
