export * from './types';
export { RetrievalClient, RetrievalError, NoPrivateProviderError } from './client';

// Convenience re-export: the system prompt fragment the AI orchestrator
// should prepend to every prompt that includes retrieved chunks.
export { RETRIEVAL_SYSTEM_PROMPT_FRAGMENT } from './system-prompt';
