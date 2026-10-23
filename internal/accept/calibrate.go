package accept

import (
	"context"

	"github.com/labdrian-ai/memorycard/internal/store"
)

// NeighborCosines returns, for every step-th memory, the highest cosine to
// any other memory that is not a part of the same chunked note. It shows how
// similar real, distinct memories get, which is what the duplicate hint
// threshold has to sit above.
func NeighborCosines(ctx context.Context, db *store.DB, step int) ([]float64, error) {
	ix, err := loadIndex(ctx, db)
	if err != nil {
		return nil, err
	}
	if step < 1 {
		step = 1
	}
	ids := make([]int64, 0, len(ix.vecs))
	for id := range ix.vecs {
		ids = append(ids, id)
	}
	var out []float64
	for n, id := range ids {
		if n%step != 0 {
			continue
		}
		best := -1.0
		for other, v := range ix.vecs {
			if other == id || ix.base[other] == ix.base[id] {
				continue
			}
			if d := dot(ix.vecs[id], v); d > best {
				best = d
			}
		}
		out = append(out, best)
	}
	return out, nil
}
