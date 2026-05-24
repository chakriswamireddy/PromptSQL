// Package embedding defines the EmbeddingProvider abstraction.
package embedding

import "context"

// Provider is the interface for generating text embeddings.
// Swapping from OpenAI to Bedrock or a local model is a one-line config change.
type Provider interface {
	// Embed returns one embedding vector per input text.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Model returns the model identifier string.
	Model() string
	// Dims returns the embedding dimensionality.
	Dims() int
}

// PayloadForColumn builds the text payload used to generate an embedding.
func PayloadForColumn(schemaName, tableName, columnName, dataType string, tags []string, description, tableComment, columnComment string) string {
	payload := schemaName + "." + tableName + "." + columnName + " (" + dataType + ")"
	if len(tags) > 0 {
		payload += " ["
		for i, t := range tags {
			if i > 0 {
				payload += ", "
			}
			payload += t
		}
		payload += "]"
	}
	if tableComment != "" {
		payload += ": " + tableComment
	}
	if columnComment != "" {
		payload += " — " + columnComment
	}
	if description != "" {
		payload += " — " + description
	}
	return payload
}
