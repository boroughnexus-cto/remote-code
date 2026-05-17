-- Add Anthropic prompt-cache metrics to pool_requests for cache hit/miss analysis.
-- cache_creation_input_tokens: tokens written to cache on this request
-- cache_read_input_tokens: tokens served from cache on this request (the win)
ALTER TABLE pool_requests ADD COLUMN cache_creation_input_tokens INTEGER DEFAULT 0;
ALTER TABLE pool_requests ADD COLUMN cache_read_input_tokens INTEGER DEFAULT 0;
