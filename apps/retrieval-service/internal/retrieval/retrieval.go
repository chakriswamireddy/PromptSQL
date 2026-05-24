// Package retrieval handles document retrieval with per-chunk ACL enforcement.
package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	pkgaudit "github.com/governance-platform/pkg/audit"
	"github.com/governance-platform/retrieval-service/internal/cache"
	"github.com/governance-platform/retrieval-service/internal/injection"
	"github.com/governance-platform/retrieval-service/internal/router"
	"github.com/governance-platform/retrieval-service/internal/store"
)

// EmbeddingProvider generates query embeddings.
type EmbeddingProvider interface {
	Embed(ctx context.Context, text, model string) ([]float32, error)
}

// Request is the input to a doc retrieval call.
type Request struct {
	Query         string
	TopK          int
	DataSourceIDs []string
	MinSimilarity float64
}

// ChunkResult is a single retrieved chunk with its injection-defense metadata.
type ChunkResult struct {
	ID             string          `json:"id"`
	CorpusID       string          `json:"corpus_id"`
	ChunkText      string          `json:"chunk_text"`
	Wrapped        string          `json:"wrapped_text"`
	Classification string          `json:"classification"`
	Similarity     float64         `json:"similarity"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	Triggers       []string        `json:"injection_triggers,omitempty"`
	Truncated      bool            `json:"truncated,omitempty"`
}

// Response is the output of a doc retrieval call.
type Response struct {
	Chunks               []ChunkResult `json:"chunks"`
	ContentClassification string       `json:"content_classification"`
	ProviderRoute        router.Route  `json:"provider_route"`
	SnapshotHash         string        `json:"snapshot_hash,omitempty"`
	QueryHash            string        `json:"query_hash"`
	PolicySetVersion     string        `json:"policy_set_version"`
}

// Service orchestrates embedding, pgvector lookup, ACL, injection defense, and routing.
type Service struct {
	store   *store.Store
	cache   *cache.Cache
	embed   EmbeddingProvider
	defense *injection.Defense
	router  *router.Router
	audit   pkgaudit.Auditor
	model   string
}

func NewService(
	st *store.Store,
	ca *cache.Cache,
	emb EmbeddingProvider,
	def *injection.Defense,
	rt *router.Router,
	audit pkgaudit.Auditor,
	model string,
) *Service {
	return &Service{
		store:   st,
		cache:   ca,
		embed:   emb,
		defense: def,
		router:  rt,
		audit:   audit,
		model:   model,
	}
}

// Retrieve runs the full retrieval pipeline for a user query.
func (s *Service) Retrieve(ctx context.Context, sess store.SessionCtx, req Request, policySetVersion string) (*Response, error) {
	if req.TopK <= 0 {
		req.TopK = 8
	}
	if req.MinSimilarity <= 0 {
		req.MinSimilarity = 0.7
	}

	queryHash := cache.QueryHash(req.Query, req.DataSourceIDs)

	// 1. Check doc-result cache.
	cacheKey := cache.DocResultKey(sess.UserID, queryHash, policySetVersion)
	var cached Response
	if hit, _ := s.cache.GetDocResult(ctx, cacheKey, &cached); hit {
		return &cached, nil
	}

	// 2. Generate (or fetch cached) query embedding.
	embedKey := cache.EmbedKey(req.Query, s.model)
	queryVec, hit, err := s.cache.GetEmbedding(ctx, embedKey)
	if err != nil || !hit {
		queryVec, err = s.embed.Embed(ctx, req.Query, s.model)
		if err != nil {
			return nil, fmt.Errorf("embed query: %w", err)
		}
		_ = s.cache.SetEmbedding(ctx, embedKey, queryVec)
	}

	// 3. pgvector ACL-aware search.
	chunks, err := s.store.FindSimilarChunks(ctx, sess, req.DataSourceIDs, queryVec, req.TopK, req.MinSimilarity)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}

	// 4. Injection defenses + collect classifications.
	var classifications []string
	var results []ChunkResult
	for _, c := range chunks {
		pairs := [][2]string{{c.ID, c.ChunkText}}
		defResults := s.defense.ApplyBatch(pairs)
		dr := defResults[0]

		classifications = append(classifications, c.Classification)
		results = append(results, ChunkResult{
			ID:             c.ID,
			CorpusID:       c.CorpusID,
			ChunkText:      dr.Sanitized,
			Wrapped:        dr.Wrapped,
			Classification: c.Classification,
			Similarity:     c.Similarity,
			Metadata:       c.Metadata,
			Triggers:       dr.Triggers,
			Truncated:      dr.Truncated,
		})
	}

	// 5. Determine max content classification and resolve LLM route.
	maxClass := router.MaxClassification(classifications)
	routes, _ := s.store.GetProviderRoutes(ctx, sess, maxClass)
	route, err := s.router.Decide(maxClass, routes)
	if err != nil {
		return nil, fmt.Errorf("route decision: %w", err)
	}

	resp := &Response{
		Chunks:                results,
		ContentClassification: maxClass,
		ProviderRoute:         route,
		QueryHash:             queryHash,
		PolicySetVersion:      policySetVersion,
	}

	// 6. Cache result (short TTL).
	_ = s.cache.SetDocResult(ctx, cacheKey, resp)

	// 7. Audit.
	chunkIDs := make([]string, len(chunks))
	for i, c := range chunks {
		chunkIDs[i] = c.ID
	}
	_ = s.audit.SystemEvent(ctx, pkgaudit.SystemEventInput{
		Action:   "retrieval.docs",
		Outcome:  "success",
		TenantID: sess.TenantID,
		ActorID:  sess.UserID,
		Metadata: map[string]any{
			"query_hash":  queryHash,
			"chunk_ids":   chunkIDs,
			"top_k":       req.TopK,
			"max_class":   maxClass,
			"provider":    route.ProviderName,
		},
	})

	return resp, nil
}

// queryHash builds a deterministic hash of a query string.
func queryHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:8])
}
