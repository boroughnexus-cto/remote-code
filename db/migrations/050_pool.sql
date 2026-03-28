-- Pool request logging for warm Claude session pool + OpenAI-compatible API
CREATE TABLE IF NOT EXISTS pool_requests (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id TEXT NOT NULL,
    model TEXT NOT NULL,
    slot_id TEXT NOT NULL,
    prompt_preview TEXT,
    tokens_in INTEGER DEFAULT 0,
    tokens_out INTEGER DEFAULT 0,
    cost_usd REAL DEFAULT 0,
    latency_ms INTEGER DEFAULT 0,
    ttft_ms INTEGER DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending',
    error_type TEXT,
    error_detail TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_pool_requests_model ON pool_requests(model);
CREATE INDEX IF NOT EXISTS idx_pool_requests_status ON pool_requests(status);
CREATE INDEX IF NOT EXISTS idx_pool_requests_created ON pool_requests(created_at);
