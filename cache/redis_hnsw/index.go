package redis_hnsw

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

func (s *Store) createOrVerifyIndex(ctx context.Context) error {
	res, err := s.client.Do(ctx, "FT.INFO", s.config.IndexName).Result()
	if err != nil {
		if isUnknownIndexErr(err) {
			return s.createIndex(ctx)
		}
		return fmt.Errorf("fail to probe redis_hnsw index: %w", err)
	}

	existingDim, err := parseDimFromFTInfo(res)
	if err != nil {
		return fmt.Errorf("fail to inspect index %s dimensions: %w", s.config.IndexName, err)
	}

	if existingDim != s.dimensions {
		return fmt.Errorf(
			"index %s dimension mismatch: existing=%d expected=%d",
			s.config.IndexName, existingDim, s.dimensions,
		)
	}

	fmt.Printf("[Info] Reusing RediSearch index: %s (dimensions=%d)\n", s.config.IndexName, existingDim)
	return nil
}

func (s *Store) createIndex(ctx context.Context) error {
	prefix := s.config.KeyPrefix + ":"
	args := []any{
		"FT.CREATE", s.config.IndexName,
		"ON", "HASH",
		"PREFIX", "1", prefix,
		"SCHEMA",
		"question", "TEXT",
		"answer", "TEXT",
		"model", "TAG", "SEPARATOR", ",",
		"tokenUsage", "NUMERIC",
		"timestamp", "NUMERIC", "SORTABLE",
		"vector", "VECTOR", "HNSW", "12",
		"TYPE", "FLOAT32",
		"DIM", strconv.Itoa(s.dimensions),
		"DISTANCE_METRIC", s.config.DistanceMetric,
		"M", strconv.Itoa(s.config.HNSWM),
		"EF_CONSTRUCTION", strconv.Itoa(s.config.HNSWEFConstruction),
		"EF_RUNTIME", strconv.Itoa(s.config.HNSWEFRuntime),
	}

	if err := s.client.Do(ctx, args...).Err(); err != nil {
		if isIndexAlreadyExistsErr(err) {
			fmt.Printf("[Info] RediSearch index %s already exists (created by peer), reusing\n", s.config.IndexName)
			return nil
		}
		return fmt.Errorf("fail to create RediSearch index: %w", err)
	}

	fmt.Printf("[Info] Created RediSearch index: %s (dimensions=%d, metric=%s)\n",
		s.config.IndexName, s.dimensions, s.config.DistanceMetric)
	return nil
}

func isUnknownIndexErr(err error) bool {
	if err == nil || err == redis.Nil {
		return err == redis.Nil
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unknown index name") || strings.Contains(msg, "no such index")
}

func isIndexAlreadyExistsErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "index already exists")
}

// parseDimFromFTInfo defensively walks the flat []any returned by FT.INFO,
// locating the "vector" attribute and extracting its "dim" value.
//
// Expected sample shape (RediSearch 2.x):
//
//	[]any{
//	  "index_name", "<name>",
//	  ...
//	  "attributes", []any{
//	    []any{"identifier","vector", "attribute","vector", "type","VECTOR",
//	          "algorithm","HNSW", "data_type","FLOAT32", "dim",int64(1024), ...},
//	    ...
//	  },
//	  ...
//	}
func parseDimFromFTInfo(res any) (int, error) {
	top, ok := res.([]any)
	if !ok {
		return 0, fmt.Errorf("unexpected FT.INFO response type %T", res)
	}

	attrs, ok := lookupValue(top, "attributes").([]any)
	if !ok {
		return 0, fmt.Errorf("FT.INFO missing attributes section")
	}

	for _, raw := range attrs {
		entry, ok := raw.([]any)
		if !ok {
			continue
		}
		identifier, _ := lookupValue(entry, "identifier").(string)
		attribute, _ := lookupValue(entry, "attribute").(string)
		if identifier != "vector" && attribute != "vector" {
			continue
		}
		dimRaw := lookupValue(entry, "dim")
		dim, err := coerceToInt(dimRaw)
		if err != nil {
			return 0, fmt.Errorf("vector attribute has invalid dim: %w", err)
		}
		return dim, nil
	}

	return 0, fmt.Errorf("FT.INFO did not include a vector attribute")
}

func lookupValue(pairs []any, key string) any {
	for i := 0; i+1 < len(pairs); i += 2 {
		k, ok := pairs[i].(string)
		if !ok {
			continue
		}
		if k == key {
			return pairs[i+1]
		}
	}
	return nil
}

func coerceToInt(v any) (int, error) {
	switch t := v.(type) {
	case int:
		return t, nil
	case int64:
		return int(t), nil
	case int32:
		return int(t), nil
	case float64:
		return int(t), nil
	case string:
		n, err := strconv.Atoi(t)
		if err != nil {
			return 0, err
		}
		return n, nil
	case nil:
		return 0, fmt.Errorf("value is nil")
	default:
		return 0, fmt.Errorf("unsupported numeric type %T", v)
	}
}

// escapeTag prefixes RediSearch TAG-special characters with a backslash so that
// model names like "gpt-4.1-mini" or "meta-llama/Llama-3" parse correctly
// inside an `@model:{...}` filter.
func escapeTag(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case ',', '.', '<', '>', '{', '}', '[', ']', '"', '\'', ':',
			';', '!', '@', '#', '$', '%', '^', '&', '*', '(', ')',
			'-', '+', '=', '~', '|', '/', '\\', ' ':
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
