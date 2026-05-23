package redis_hnsw

import (
	"encoding/binary"
	"math"
)

// float32SliceToBytes serializes a []float32 into the little-endian byte layout
// that RediSearch expects for VECTOR fields declared with TYPE FLOAT32.
func float32SliceToBytes(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}
