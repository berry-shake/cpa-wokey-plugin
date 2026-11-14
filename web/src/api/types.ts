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
  /** Set when proxy_url is exactly the address of a proxy in the list. */
  proxy_id?: string;
  proxy_name?: string;
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
  /** "" removes the note. */
  note?: string;
  proxy_url?: string;
  /** Uses the address of this list proxy; wins over proxy_url. */
  proxy_id?: string;
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
  /** Generation speed: (output − 1) ÷ (total − TTFT); streams only. */
  output_tokens_per_second?: number;
  /** End-to-end speed: output ÷ total, the wait for the first token included. */
  e2e_tokens_per_second?: number;
  error_code?: string;
  error?: string;
  error_phase?: string;
  // Since 0.1.9; absent on older entries.
  client_path?: string;
  user_agent?: string;
  auth_index?: string;
  /** "direct", "env" or the proxy as scheme://host:port. */
  egress?: string;
  /** What Wokey answered; 0 or absent when no response arrived. */
  upstream_status?: number;
  upstream_request_id?: string;
  retry_after?: number;
  finish_reason?: string;
  frames?: number;
  last_frame_ms?: number;
  /** Set only once Wokey answered 2xx: whether the response carried usage. */
  usage_reported?: boolean;
  /** How many entries share the request ID (credentials CPA tried). */
  attempts?: number;
}

export interface LogSummary {
  requests: number;
  success: number;
  failed: number;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  cache_hit_rate: number | null;
  /** Medians of the two speeds over the successful requests. */
  output_tokens_per_second: number | null;
  e2e_tokens_per_second: number | null;
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

/** A saved proxy; url has its password redacted. */
export interface ProxyView {
  id: string;
  name: string;
  url: string;
  /** scheme://host:port */
  label: string;
  created_at?: string;
  /** The latest check of the current address, saved by the plugin. */
  last_check?: ProxyCheckResult;
}

export interface ProxiesResponse {
  proxies: ProxyView[];
  /** CPA's global proxy-url, redacted; absent when not set. */
  global_proxy?: string;
  /** The latest checks of the two built-in rows. */
  builtin_checks?: Partial<Record<"direct" | "inherit", ProxyCheckResult>>;
  load_error?: string;
}

/** Body of POST (add, without id) and PATCH (edit) /proxies. */
export interface ProxySaveRequest {
  id?: string;
  name?: string;
  /** Absent when editing keeps the saved address. */
  url?: string;
}

export interface ProxySaveResponse {
  ok: boolean;
  proxy?: ProxyView;
  /** Keys moved to the new address when the address changed. */
  updated_credentials?: number;
  failed_credentials?: string[];
}

export interface ProxyImportResponse {
  ok: boolean;
  added: number;
  proxies: ProxyView[];
}

/** Response of POST /proxies/order: the list in its saved order. */
export interface ProxyOrderResponse {
  ok: boolean;
  proxies: ProxyView[];
}

export type ProxyCheckTarget = { id: string } | { target: "direct" | "inherit" };

/** An egress check; a failed check is ok: false with error, still HTTP 200. */
export interface ProxyCheckResult {
  ok: boolean;
  /** "direct", "env" or the proxy as scheme://host:port. */
  egress?: string;
  ip?: string;
  /** ISO 3166-1 alpha-2 code when the lookup service gave one. */
  country?: string;
  region?: string;
  city?: string;
  latency_ms?: number;
  source?: string;
  checked_at?: string;
  error?: string;
}

export interface ErrorResponse {
  error?: string;
  message?: string;
}
