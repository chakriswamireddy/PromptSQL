package main

import (
	"os"
	"strconv"
	"time"
)

type config struct {
	HTTPAddr     string
	Environment  string
	Version      string
	OTLPEndpoint string
	SamplingRate float64
	UnleashURL   string
	UnleashToken string

	DatabaseURL string
	RedisURL    string

	// Crawler settings.
	CrawlInterval    time.Duration
	CrawlConcurrency int
	SampleMaxRows    int

	// Embedding settings.
	OpenAIAPIKey       string
	EmbeddingModel     string
	EmbeddingDims      int
	EmbeddingBatchSize int
	EmbeddingWorkers   int

	// Budget cap: max USD cost allowed per tenant per day.
	EmbeddingBudgetUSD float64
}

func loadConfig() config {
	sr, _ := strconv.ParseFloat(getEnv("OTEL_SAMPLING_RATE", "1.0"), 64)
	crawlInterval, _ := time.ParseDuration(getEnv("CRAWL_INTERVAL", "6h"))
	crawlConc, _ := strconv.Atoi(getEnv("CRAWL_CONCURRENCY", "4"))
	sampleMax, _ := strconv.Atoi(getEnv("SAMPLE_MAX_ROWS", "10"))
	embDims, _ := strconv.Atoi(getEnv("EMBEDDING_DIMS", "1536"))
	embBatch, _ := strconv.Atoi(getEnv("EMBEDDING_BATCH_SIZE", "100"))
	embWorkers, _ := strconv.Atoi(getEnv("EMBEDDING_WORKERS", "4"))
	embBudget, _ := strconv.ParseFloat(getEnv("EMBEDDING_BUDGET_USD", "10.0"), 64)

	return config{
		HTTPAddr:           getEnv("HTTP_ADDR", ":8082"),
		Environment:        getEnv("DEPLOYMENT_ENVIRONMENT", "local"),
		Version:            getEnv("OTEL_SERVICE_VERSION", "local"),
		OTLPEndpoint:       getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		SamplingRate:       sr,
		UnleashURL:         getEnv("UNLEASH_URL", "http://localhost:4242/api"),
		UnleashToken:       getEnv("UNLEASH_API_TOKEN", ""),
		DatabaseURL:        getEnv("DATABASE_URL", "postgres://app_write:app_write@localhost:5432/governance?sslmode=disable"),
		RedisURL:           getEnv("REDIS_URL", "redis://localhost:6379/0"),
		CrawlInterval:      crawlInterval,
		CrawlConcurrency:   crawlConc,
		SampleMaxRows:      sampleMax,
		OpenAIAPIKey:       getEnv("OPENAI_API_KEY", ""),
		EmbeddingModel:     getEnv("EMBEDDING_MODEL", "text-embedding-3-small"),
		EmbeddingDims:      embDims,
		EmbeddingBatchSize: embBatch,
		EmbeddingWorkers:   embWorkers,
		EmbeddingBudgetUSD: embBudget,
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
