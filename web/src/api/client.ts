import type {
  CredentialSaveRequest,
  CredentialSaveResponse,
  ErrorResponse,
  LogPage,
  ModelsResponse,
  QuotaView,
  StatusView,
} from "./types";

export const API_BASE = "/v0/management/plugins/wokey";
export const CPA_BASE = "/v0/management";

/** A failed request; status is the HTTP status (absent for network errors). */
export class ApiError extends Error {
  status?: number;
  constructor(message: string, status?: number) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

// CPA rejects a missing, wrong or banned management key with 401/403 before
// the request reaches the plugin. The plugin itself never answers 401/403.
export function isAuthError(error: unknown): boolean {
  const status = (error as { status?: unknown } | null)?.status;
  return status === 401 || status === 403;
}

/** The HTTP status of a failed request, or undefined for network errors. */
export function errorStatus(error: unknown): number | undefined {
  const status = (error as { status?: unknown } | null)?.status;
  return typeof status === "number" ? status : undefined;
}

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export interface RequestOptions {
  method?: string;
  body?: unknown;
}

/**
 * Sends a management request with the key as a bearer token. The response is
 * read as text and parsed as JSON; a non-JSON body becomes {message: text}.
 */
export async function request<T>(url: string, key: string, options?: RequestOptions): Promise<T> {
  const headers: Record<string, string> = { Authorization: "Bearer " + key };
  const init: RequestInit = { method: options?.method || "GET", cache: "no-store", headers };
  if (options && options.body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(options.body);
  }
  const response = await fetch(url, init);
  const text = await response.text();
  let data: unknown;
  try {
    data = JSON.parse(text);
  } catch {
    data = { message: text || `HTTP ${response.status}` };
  }
  if (!response.ok) {
    const body = data as ErrorResponse | null;
    throw new ApiError((body && (body.message || body.error)) || `HTTP ${response.status}`, response.status);
  }
  return data as T;
}

/** The plugin's and CPA's management endpoints, bound to a key getter. */
export function createApi(getKey: () => string) {
  const api = <T>(path: string, options?: RequestOptions) => request<T>(API_BASE + path, getKey(), options);
  const cpa = <T>(path: string, options?: RequestOptions) => request<T>(CPA_BASE + path, getKey(), options);
  return {
    status: () => api<StatusView>("/status"),
    models: () => api<ModelsResponse>("/models"),
    refreshModels: () => api<ModelsResponse>("/models/refresh", { method: "POST", body: {} }),
    quota: (authIndex: string) => api<QuotaView>("/credentials/quota?auth_index=" + encodeURIComponent(authIndex)),
    saveCredential: (adding: boolean, body: CredentialSaveRequest) =>
      api<CredentialSaveResponse>("/credentials", { method: adding ? "POST" : "PATCH", body }),
    logs: (params: URLSearchParams) => api<LogPage>("/logs?" + params.toString()),
    clearLogs: () => api<unknown>("/logs", { method: "DELETE" }),
    setDisabled: (name: string, authIndex: string, disabled: boolean) =>
      cpa<unknown>("/auth-files/status", { method: "PATCH", body: { name, auth_index: authIndex, disabled } }),
    deleteAuthFile: (name: string) => cpa<unknown>("/auth-files?name=" + encodeURIComponent(name), { method: "DELETE" }),
  };
}

export type Api = ReturnType<typeof createApi>;
