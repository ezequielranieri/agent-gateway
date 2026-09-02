-- +goose Up
-- 0014_pricing_tables.sql
-- Pricing tables for model pricing with versioning

-- Create schema
CREATE SCHEMA IF NOT EXISTS pricing;

-- Pricing versions table
CREATE TABLE pricing.versions (
    version       VARCHAR(50) PRIMARY KEY,
    description   TEXT,
    effective_date DATE NOT NULL,
    is_default    BOOLEAN DEFAULT false,
    created_at    TIMESTAMPTZ DEFAULT now(),
    updated_at    TIMESTAMPTZ DEFAULT now()
);

-- Pricing models table
CREATE TABLE pricing.models (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    version       VARCHAR(50) NOT NULL REFERENCES pricing.versions(version) ON DELETE CASCADE,
    provider      VARCHAR(50) NOT NULL,
    model_name    VARCHAR(100) NOT NULL,
    input_price_per_1k   NUMERIC(10,6) NOT NULL,
    output_price_per_1k  NUMERIC(10,6) NOT NULL,
    effective_date DATE,
    is_active     BOOLEAN DEFAULT true,
    created_at    TIMESTAMPTZ DEFAULT now(),
    updated_at    TIMESTAMPTZ DEFAULT now(),
    UNIQUE (version, provider, model_name)
);

-- Indexes for fast lookups
CREATE INDEX idx_pricing_models_lookup ON pricing.models (version, provider, model_name);
CREATE INDEX idx_pricing_models_active ON pricing.models (version, provider, model_name) WHERE is_active;

-- Create trigger function for updated_at
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION pricing.update_updated_at_column()
RETURNS TRIGGER LANGUAGE plpgsql AS $func$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$func$;

CREATE TRIGGER update_pricing_models_updated_at
    BEFORE UPDATE ON pricing.models
    FOR EACH ROW EXECUTE FUNCTION pricing.update_updated_at_column();

CREATE TRIGGER update_pricing_versions_updated_at
    BEFORE UPDATE ON pricing.versions
    FOR EACH ROW EXECUTE FUNCTION pricing.update_updated_at_column();
-- +goose StatementEnd

-- Seed data: versions
INSERT INTO pricing.versions (version, description, effective_date, is_default) VALUES
('2024-01-openai', 'OpenAI pricing as of January 2024', '2024-01-01', true),
('2024-01-anthropic', 'Anthropic pricing as of January 2024', '2024-01-01', false),
('2024-01-ollama', 'Ollama local models (zero cost)', '2024-01-01', false)
ON CONFLICT (version) DO UPDATE SET
    description = EXCLUDED.description,
    effective_date = EXCLUDED.effective_date,
    is_default = EXCLUDED.is_default,
    updated_at = now();

-- Seed data: models - OpenAI
INSERT INTO pricing.models (version, provider, model_name, input_price_per_1k, output_price_per_1k, effective_date) VALUES
('2024-01-openai', 'openai', 'gpt-4', 0.03, 0.06, '2024-01-01'),
('2024-01-openai', 'openai', 'gpt-4-turbo', 0.01, 0.03, '2024-01-01'),
('2024-01-openai', 'openai', 'gpt-3.5-turbo', 0.0005, 0.0015, '2024-01-01')
ON CONFLICT (version, provider, model_name) DO UPDATE SET
    input_price_per_1k = EXCLUDED.input_price_per_1k,
    output_price_per_1k = EXCLUDED.output_price_per_1k,
    updated_at = now();

-- Anthropic models
INSERT INTO pricing.models (version, provider, model_name, input_price_per_1k, output_price_per_1k, effective_date) VALUES
('2024-01-anthropic', 'anthropic', 'claude-3-opus-20240229', 0.015, 0.075, '2024-01-01'),
('2024-01-anthropic', 'anthropic', 'claude-3-sonnet-20240229', 0.003, 0.015, '2024-01-01'),
('2024-01-anthropic', 'anthropic', 'claude-3-haiku-20240307', 0.00025, 0.00125, '2024-01-01')
ON CONFLICT (version, provider, model_name) DO UPDATE SET
    input_price_per_1k = EXCLUDED.input_price_per_1k,
    output_price_per_1k = EXCLUDED.output_price_per_1k,
    updated_at = now();

-- Ollama models (zero cost)
INSERT INTO pricing.models (version, provider, model_name, input_price_per_1k, output_price_per_1k, effective_date) VALUES
('2024-01-ollama', 'ollama', 'llama3', 0.0, 0.0, '2024-01-01'),
('2024-01-ollama', 'ollama', 'mistral', 0.0, 0.0, '2024-01-01'),
('2024-01-ollama', 'ollama', 'codellama', 0.0, 0.0, '2024-01-01')
ON CONFLICT (version, provider, model_name) DO UPDATE SET
    input_price_per_1k = EXCLUDED.input_price_per_1k,
    output_price_per_1k = EXCLUDED.output_price_per_1k,
    updated_at = now();

-- Grant permissions (adjust roles as needed)
GRANT USAGE ON SCHEMA pricing TO gateway;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA pricing TO gateway;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA pricing TO gateway;

-- +goose Down
-- Drop triggers
DROP TRIGGER IF EXISTS update_pricing_models_updated_at ON pricing.models;
DROP TRIGGER IF EXISTS update_pricing_versions_updated_at ON pricing.versions;
DROP FUNCTION IF EXISTS pricing.update_updated_at_column();

DROP TABLE IF EXISTS pricing.models;
DROP TABLE IF EXISTS pricing.versions;
DROP SCHEMA IF EXISTS pricing;