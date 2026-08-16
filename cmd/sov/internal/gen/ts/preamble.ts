// ---- Errors ----------------------------------------------------------------
export type ErrorCode =
  | "NOT_FOUND" | "FORBIDDEN" | "UNAUTHORIZED" | "BAD_REQUEST"
  | "CONFLICT" | "INTERNAL" | "NOT_IMPLEMENTED" | "RATE_LIMITED"
  | "ROLE_CONFLICT" | "INVALID_SEAL";

export interface SovError extends Error {
  message: string;
  code?: ErrorCode;
  error_code?: string;
  status: number;
}

// ---- Runtime client --------------------------------------------------------
export interface SovClientOptions {
  baseURL: string;
  token?: string;
  fetch?: typeof fetch;                       // injectable for tests / SSR
  wireShape?: "named" | "positional";         // default "named"
  headers?: Record<string, string>;            // extra per-call headers
}

// ---- Batch -----------------------------------------------------------------
// One POST -> N method calls. Gateway groups by destination and rebatches
// per-service automatically (see gateway/batch.go). Per-entry result is a
// discriminated union: success carries `data`, failure carries `error`.
//
// Services is the catalog of every router → method → {params, result}.
// The generator augments this interface inside this namespace via TS
// declaration merging so batch() is fully typed:
//
//   { service: "Auth"; method: "login" } → args narrows to AuthLoginParams,
//                                          result narrows to AuthLoginResult.
//
// Consumers don't write Services entries by hand — they come from sovgen.
export interface Services { }

export type ServiceName = Extract<keyof Services, string>;
type MethodName<S extends ServiceName> = Extract<keyof Services[S], string>;

// EntryFor narrows args based on the {service, method} pair. Methods
// with no params get optional args; methods with params require them.
type EntryFor<S extends ServiceName, M extends MethodName<S>> =
  Services[S][M] extends { params: infer P }
  ? [P] extends [void] | [undefined] | [never]
  ? { service: S; method: M; args?: undefined }
  : { service: S; method: M; args: P }
  : { service: S; method: M; args?: unknown };

// BatchEntry is the discriminated union over every (service, method)
// pair the gateway exposes. Pick a service → method autocompletes;
// pick a method → args narrows to the exact params interface.
export type BatchEntry =
  Services extends Record<string, never>
  ? { service: string; method: string; args?: unknown } // pre-sovgen fallback
  : { [S in ServiceName]: { [M in MethodName<S>]: EntryFor<S, M> }[MethodName<S>] }[ServiceName];

// ResultOf pulls the bare success type out of Services for a single
// entry — used by invoke() which throws on error.
export type ResultOf<E> =
  E extends { service: infer S; method: infer M }
  ? S extends ServiceName
  ? M extends MethodName<S>
  ? Services[S][M] extends { result: infer R }
  ? R
  : unknown
  : unknown
  : unknown
  : unknown;

// ResultFor wraps ResultOf in the discriminated BatchResult — used by
// batch() where per-entry failure is normal.
type ResultFor<E> = BatchResult<ResultOf<E>>;

export type BatchResult<T = unknown> =
  | { data: T; error?: undefined }
  | { data?: undefined; error: SovError };

export interface BatchOptions {
  signal?: AbortSignal;
}

export class SovClient {
  constructor(private opts: SovClientOptions) { }

  setToken(t: string | null): void {
    this.opts.token = t ?? undefined;
  }

  /** Swap the gateway base URL at runtime (e.g. dev → prod). */
  setBaseURL(url: string): void {
    this.opts.baseURL = url;
  }

  /** Current base URL — what every call() and batch() points at. */
  get baseURL(): string {
    return this.opts.baseURL;
  }

  /**
   * Send N method calls in one HTTP POST to /rpc/_batch. The gateway
   * groups entries by destination service and rebatches per-pod, so
   * 4 calls to one downstream pod become 1 nested batch POST.
   *
   * The result map preserves the caller-supplied alias keys and tags
   * each entry as { data } or { error }. Network / 5xx failures throw;
   * per-entry RPC errors land in the result map as `{ error: SovError }`.
   *
   * @example
   *   const r = await cli.batch({
   *     me:    { service: "User", method: "getMe" },
   *     feed:  { service: "Feed", method: "timeline", args: { limit: 10 } },
   *   });
   *   if (r.me.error) console.error(r.me.error.code);
   *   else            console.log(r.me.data);
   */
  async batch<T extends Record<string, BatchEntry>>(
    calls: T,
    opts?: BatchOptions,
  ): Promise<{ [K in keyof T]: ResultFor<T[K]> }> {
    const f = this.opts.fetch ?? fetch;
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      ...(this.opts.headers ?? {}),
    };
    if (this.opts.token) {
      headers["Authorization"] = `Bearer ${this.opts.token}`;
    }
    const resp = await f(`${this.opts.baseURL}/rpc/_batch`, {
      method: "POST",
      headers,
      body: JSON.stringify({ calls }),
      signal: opts?.signal,
    });
    if (resp.status >= 500) {
      const txt = await resp.text();
      const err = new Error(`batch HTTP ${resp.status}: ${txt}`) as SovError;
      err.status = resp.status;
      throw err;
    }
    const parsed = (await resp.json()) as {
      results?: Record<string, {
        data?: unknown;
        error?: { message: string; code?: ErrorCode; error_code?: string };
      }>;
    };
    const out: Record<string, BatchResult> = {};
    for (const alias of Object.keys(calls)) {
      const r = parsed.results?.[alias];
      if (r?.error) {
        const err = new Error(r.error.message) as SovError;
        err.code = r.error.code;
        err.error_code = r.error.error_code;
        err.status = 0;
        out[alias] = { error: err };
      } else {
        out[alias] = { data: r?.data };
      }
    }
    return out as { [K in keyof T]: ResultFor<T[K]> };
  }

  /**
   * Type-narrowed single-entry call. Same shape as a batch entry —
   * pick service → method autocompletes, args narrows, result narrows.
   * Translates to `POST {baseURL}/rpc/{service}/{method}` exactly like
   * the per-router class methods (cli.Auth.login(...)). Errors throw
   * `SovError`; use batch() if you want per-entry failure isolation.
   *
   * @example
   *   const r = await cli.invoke({
   *     service: "Hello", method: "something",
   *     args: { name: "world" },
   *   });
   *   // r is narrowed via Services.Hello.something.result.
   */
  async invoke<E extends BatchEntry>(entry: E): Promise<ResultOf<E>> {
    // BatchEntry's union arms carry LITERAL service/method strings, so
    // accessing entry.service on a generic E confuses TS prop lookup.
    // Widen via typed assignment — no `as` cast, type-checker verifies
    // the shape against E's constraint.
    const e: { service: string; method: string; args?: unknown } = entry;
    return this.call<ResultOf<E>>(e.service, e.method, e.args);
  }

  async call<T>(router: string, method: string, params?: unknown): Promise<T> {
    const args = this.opts.wireShape === "positional"
      ? [params]
      : (params ?? {});
    const f = this.opts.fetch ?? fetch;
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      ...(this.opts.headers ?? {}),
    };
    if (this.opts.token) {
      headers["Authorization"] = `Bearer ${this.opts.token}`;
    }
    const resp = await f(`${this.opts.baseURL}/rpc/${router}/${method}`, {
      method: "POST",
      headers,
      body: JSON.stringify({ args }),
    });
    const text = await resp.text();
    const parsed = text ? JSON.parse(text) : {};
    if (resp.status >= 400) {
      const e = (parsed.error ?? {}) as Partial<SovError>;
      const err = new Error(e.message ?? `HTTP ${resp.status}`) as SovError;
      err.code = e.code as ErrorCode | undefined;
      err.error_code = e.error_code;
      err.status = resp.status;
      throw err;
    }
    return parsed.data as T;
  }
}
