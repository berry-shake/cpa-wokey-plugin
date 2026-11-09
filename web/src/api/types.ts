// JSON shapes served by the plugin's management API. Field names match the Go
// structs' json tags; optional fields are the ones tagged omitempty.

export interface PluginConfigView {
  base_url: string;
  default_prefix: string;
  /** Mirrors CPA's routing.force-model-prefix; absent when unknown. */
  force_model_prefix?: boolean;
  models_refresh_minutes: number;
  request_timeout_seconds: number;
  stream_idle_timeout_seconds: number;
  global_proxy?: string;
}

export interface CatalogMeta {
  count: number;
  revision?: string;
  fetched_at?: string;
  checked_at?: string;
  last_error?: string;
}

export interface CredentialView {
  auth_index: string;
  id: string;
  name: string;
  label: string;
  key_masked: string;
  disabled: boolean;
  unavailable: boolean;
  status?: string;
  status_message?: string;
  priority: number;
  note?: string;
  base_url?: string;
  proxy_url?: string;
  issuer_token_masked?: string;
  /** null means the plugin's default prefix applies. */
  prefix: string | null;
  effective_prefix: string;
  all_models: boolean;
  models: string[] | null;
  missing_models?: string[];
  registered_models: number;
  registered_known: boolean;
  success: number;
  failed: number;
  read_error?: string;
}

export interface StatusView {
  plugin: string;
  version: string;
  now: string;
  config: PluginConfigView;
  catalog: CatalogMeta;
  credentials: CredentialView[];
  list_error?: string;
}

export interface ModelPricing {
  input?: number;
  output?: number;
  cache_read?: number;
  cache_write?: number;
}

export interface ModelView {
  id: string;
  name?: string;
  owned_by?: string;
  context_length?: number;
  max_completion_tokens?: number;
  input_modalities?: string[];
  output_modalities?: string[];
  pricing: ModelPricing;
}

export interface ModelsResponse {
  catalog: CatalogMeta;
  models: ModelView[];
}

export interface BalanceView {
  account?: string;
  available_usd: number;
  reserved_usd: number;
}

export interface KeyUsageView {
  label?: string;
  usage_usd: number;
  daily_usd: number;
  weekly_usd: number;
  monthly_usd: number;
  limit_usd?: number;
  limit_remaining_usd?: number;
  limit_reset?: string;
}

export interface IssuerView {
  account: string;
  /** Absent when the token has no Provider account. */
  provider_usd?: number;
  provider_frozen?: boolean;
  referrer_usd: number;
}

export interface QuotaView {
  balance?: BalanceView;
  balance_error?: string;
  key?: KeyUsageView;
  key_error?: string;
  issuer?: IssuerView;
  issuer_error?: string;
  fetched_at: string;
}

/** Body of POST (add) and PATCH (edit) /credentials. */
export interface CredentialSaveRequest {
  auth_index?: string;
  name?: string;
  api_key?: string;
  skip_verify?: boolean;
  label?: string;
  proxy_url?: string;
  issuer_token?: string;
  /** null resets to the plugin default. */
  prefix?: string | null;
  /** null means all models. */
  models?: string[] | null;
  /** null clears the priority. */
  priority?: number | null;
}

export interface CredentialSaveResponse {
  ok: boolean;
  credential?: CredentialView;
  unknown_models?: string[];
}

export interface LogEntry {
  id: string;
  request_id?: string;
  time: string;
  endpoint: string;
  model: string;
  upstream_model?: string;
  served_model?: string;
  reasoning_effort?: string;
  stream: boolean;
  status: string;
  http_status?: number;
  credential?: string;
  auth_id?: string;
  duration_ms: number;
  ttft_ms?: number;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  cache_write_tokens?: number;
  reasoning_tokens?: number;
  output_tokens_per_second?: number;
  error_code?: string;
  error?: string;
  error_phase?: string;
}

export interface LogSummary {
  requests: number;
  success: number;
  failed: number;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  cache_hit_rate: number | null;
  output_tokens_per_second: number | null;
}

export interface LogPage {
  items: LogEntry[];
  total: number;
  offset: number;
  retention: number;
  retention_days: number;
  summary: LogSummary;
  error?: string;
}

export interface ErrorResponse {
  error?: string;
  message?: string;
}
