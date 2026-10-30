// Package bench runs memorycard against public benchmarks. Nothing in the
// product depends on it.
package bench

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"

	"github.com/labdrian-ai/memorycard/internal/embed"
	_ "modernc.org/sqlite"
)

// Cache is an embed.Embedder that remembers every vector it computed, keyed by
// model, kind and text. Embedding on a CPU is the slow part of a benchmark run,
// so a rerun or a resumed run only embeds texts it has never seen.
type Cache struct {
	inner        embed.Embedder
	db           *sql.DB
	hits, misses int
}

// OpenCache opens or creates the cache file at path in front of inner.
func OpenCache(path string, inner embed.Embedder) (*Cache, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`create table if not exists vectors (key text primary key, vec blob not null)`); err != nil {
		db.Close()
		return nil, err
	}
	return &Cache{inner: inner, db: db}, nil
}

// Close closes the cache file.
func (c *Cache) Close() error { return c.db.Close() }

// Model implements embed.Embedder.
func (c *Cache) Model() string { return c.inner.Model() }

// Stats returns how many texts this handle served from the cache and how many
// it had to embed.
func (c *Cache) Stats() (hits, misses int) { return c.hits, c.misses }

// Embed implements embed.Embedder. Texts not in the cache go to the inner
// embedder in one request, each distinct text once.
func (c *Cache) Embed(ctx context.Context, texts []string, kind embed.Kind) ([][]float32, error) {
	out := make([][]float32, len(texts))
	keys := make([]string, len(texts))
	var missing []string
	pending := map[string][]int{}
	for i, t := range texts {
		keys[i] = c.key(t, kind)
		var blob []byte
		err := c.db.QueryRowContext(ctx, `select vec from vectors where key = ?`, keys[i]).Scan(&blob)
		switch {
		case err == nil:
			out[i] = decode(blob)
			c.hits++
		case err == sql.ErrNoRows:
			if _, seen := pending[keys[i]]; !seen {
				missing = append(missing, t)
			}
			pending[keys[i]] = append(pending[keys[i]], i)
		default:
			return nil, err
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	vecs, err := c.inner.Embed(ctx, missing, kind)
	if err != nil {
		return nil, err
	}
	if len(vecs) != len(missing) {
		return nil, fmt.Errorf("embedder returned %d vectors for %d texts", len(vecs), len(missing))
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for j, t := range missing {
		k := c.key(t, kind)
		if _, err := tx.ExecContext(ctx, `insert or replace into vectors(key, vec) values (?, ?)`, k, encode(vecs[j])); err != nil {
			return nil, err
		}
		for _, i := range pending[k] {
			out[i] = vecs[j]
		}
		c.misses++
	}
	return out, tx.Commit()
}

func (c *Cache) key(text string, kind embed.Kind) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d\x00", c.inner.Model(), kind)
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))
}

func encode(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func decode(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}
