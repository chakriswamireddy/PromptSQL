package embedding

import (
	"context"
	"fmt"

	openai "github.com/sashabaranov/go-openai"
)

// OpenAIProvider implements Provider using the OpenAI embeddings API.
type OpenAIProvider struct {
	client *openai.Client
	model  string
	dims   int
}

// NewOpenAI creates an OpenAIProvider. model should be "text-embedding-3-small" or "text-embedding-3-large".
func NewOpenAI(apiKey, model string, dims int) *OpenAIProvider {
	return &OpenAIProvider{
		client: openai.NewClient(apiKey),
		model:  model,
		dims:   dims,
	}
}

func (o *OpenAIProvider) Model() string { return o.model }
func (o *OpenAIProvider) Dims() int     { return o.dims }

// Embed batches texts and returns embeddings in the same order.
func (o *OpenAIProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	req := openai.EmbeddingRequestStrings{
		Input:      texts,
		Model:      openai.EmbeddingModel(o.model),
		Dimensions: o.dims,
	}

	resp, err := o.client.CreateEmbeddingsWithStrings(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("openai embed: %w", err)
	}

	out := make([][]float32, len(resp.Data))
	for i, d := range resp.Data {
		out[i] = d.Embedding
	}
	return out, nil
}

// NoopProvider is a stub used in CI / tests when no real API key is available.
type NoopProvider struct {
	dims int
}

func NewNoop(dims int) *NoopProvider { return &NoopProvider{dims: dims} }

func (n *NoopProvider) Model() string { return "noop" }
func (n *NoopProvider) Dims() int     { return n.dims }

func (n *NoopProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, n.dims)
	}
	return out, nil
}
