package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

func BindJSON(r *http.Request, obj interface{}) error {
	if r.Body == nil {
		return errors.New("request body is empty")
	}
	defer r.Body.Close()

	decoder := json.NewDecoder(r.Body)
	// decoder.DisallowUnknownFields()

	err := decoder.Decode(obj)
	if err != nil {
		return fmt.Errorf("json decode error: %w", err)
	}

	return nil
}
