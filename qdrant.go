package main

import (
	"context"
	"fmt"

	"github.com/qdrant/go-client/qdrant"
)

func CreateQdrantcollection(host string, port int16, collection_name string) error {
	client, err := qdrant.NewClient(&qdrant.Config{
		Host: qdrant_host,
		Port: qdrant_client_port,
	})
	if err != nil {
		return fmt.Errorf("Fail to create qdrant client: %s", err)
	}

	is_exist, err := client.CollectionExists(context.Background(), collection_name)
	if err != nil {
		return fmt.Errorf("Fail to check if collection %s exist or not: %s", collection_name, err)
	}
	if !is_exist {
		err = client.CreateCollection(context.Background(), &qdrant.CreateCollection{
			CollectionName: collection_name,
			VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{
				Size:     1536,
				Distance: qdrant.Distance_Cosine,
			}),
		})
		if err != nil {
			return fmt.Errorf("Fail to create collection")
		}

		return nil
	} else {
		return nil
	}
}
