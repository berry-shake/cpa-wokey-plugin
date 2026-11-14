// The Wokey shapes of the management API: the catalog model, the quota of a
// key, and the issuing token added to keys, their save body and the editor.

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

export interface CredentialExtra {
  issuer_token_masked?: string;
}

export interface CatalogExtra {}

export interface CredentialSaveExtra {
  issuer_token?: string;
}

/** The issuing token fields of the key editor. */
export interface EditorExtra {
  /** The token input; the saved token is never sent to the page. */
  issuer: string;
  /** Removes the saved token. */
  issuerClear: boolean;
  issuerPlaceholder: string;
  /** The masked saved token; "" when the key has none. */
  issuerSaved: string;
}
