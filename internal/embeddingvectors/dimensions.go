package embeddingvectors

import "fmt"

const CanonicalDimensions = 3072

func PadToLength(vec []float32, dim int) []float32 {
	if len(vec) >= dim {
		return vec
	}
	padded := make([]float32, dim)
	copy(padded, vec)
	return padded
}

func EnsureCanonicalDimensions(vec []float32) ([]float32, error) {
	if len(vec) > CanonicalDimensions {
		return nil, fmt.Errorf("embedding vector length %d exceeds canonical dimension %d", len(vec), CanonicalDimensions)
	}
	return PadToLength(vec, CanonicalDimensions), nil
}
