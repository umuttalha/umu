interface UmutDBConfig {
  url: string;
  token: string;
}

interface QueryResult {
  columns: string[];
  rows: any[][];
  lastInsertId: number;
  rowsAffected: number;
}

interface BatchQuery {
  query: string;
  params?: any[];
}

class UmutDBError extends Error {
  constructor(message: string, public status: number) {
    super(message);
    this.name = "UmutDBError";
  }
}

export class UmutDB {
  private url: string;
  private headers: Record<string, string>;

  constructor(config: UmutDBConfig) {
    this.url = config.url.replace(/\/$/, "");
    this.headers = {
      "Content-Type": "application/json",
      Authorization: `Bearer ${config.token}`,
    };
  }

  async query<T = any[]>(
    sql: string,
    params?: any[]
  ): Promise<{ columns: string[]; rows: T[] }> {
    const result = await this.request<QueryResult>({
      query: sql,
      params: params ?? [],
    });
    return { columns: result.columns, rows: result.rows as T[] };
  }

  async execute(
    sql: string,
    params?: any[]
  ): Promise<{ lastInsertId: number; rowsAffected: number }> {
    const result = await this.request<QueryResult>({
      query: sql,
      params: params ?? [],
    });
    return {
      lastInsertId: result.lastInsertId,
      rowsAffected: result.rowsAffected,
    };
  }

  async batch(queries: BatchQuery[]): Promise<QueryResult[]> {
    return this.request<QueryResult[]>({
      query: "",
      many: queries.map((q) => ({ query: q.query, params: q.params ?? [] })),
    });
  }

  async transaction(queries: BatchQuery[]): Promise<QueryResult[]> {
    return this.request<QueryResult[]>({
      query: "",
      tx: {
        queries: queries.map((q) => ({
          query: q.query,
          params: q.params ?? [],
        })),
      },
    });
  }

  private async request<T>(body: Record<string, any>): Promise<T> {
    const response = await fetch(this.url, {
      method: "POST",
      headers: this.headers,
      body: JSON.stringify(body),
    });

    if (!response.ok) {
      const err = await response.json().catch(() => ({ error: response.statusText }));
      throw new UmutDBError(err.error || "request failed", response.status);
    }

    return response.json();
  }
}

export { UmutDBError };
export type { UmutDBConfig, QueryResult, BatchQuery };
