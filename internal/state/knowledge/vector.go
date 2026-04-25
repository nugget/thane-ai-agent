package knowledge

import (
	"encoding/binary"
	"math"
)

// EncodeEmbedding converts a float32 slice to bytes for storage.
func EncodeEmbedding(embedding []float32) []byte {
	if len(embedding) == 0 {
		return nil
	}
	buf := make([]byte, len(embedding)*4)
	for i, v := range embedding {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// DecodeEmbedding converts bytes back to a float32 slice.
func DecodeEmbedding(data []byte) []float32 {
	if len(data) == 0 {
		return nil
	}
	result := make([]float32, len(data)/4)
	for i := range result {
		result[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return result
}
