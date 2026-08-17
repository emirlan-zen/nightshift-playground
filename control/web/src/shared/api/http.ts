export class ApiError<T = unknown> extends Error {
  constructor(
    public status: number,
    message: string,
    public body?: T,
  ) {
    super(message);
    this.name = "ApiError";
  }
}
async function failure(response: Response): Promise<ApiError> {
  const text = await response.text();
  let body: unknown;
  try {
    body = JSON.parse(text);
  } catch {
    body = undefined;
  }
  const message =
    typeof body === "object" && body && "error" in body
      ? String((body as { error: unknown }).error)
      : text || response.statusText;
  return new ApiError(response.status, message, body);
}
export async function getJSON<T>(url: string): Promise<T> {
  const response = await fetch(url, { cache: "no-store" });
  if (!response.ok) throw await failure(response);
  return response.json() as Promise<T>;
}

export async function getText(url: string): Promise<string> {
  const response = await fetch(url, { cache: "no-store" });
  if (!response.ok) throw await failure(response);
  return response.text();
}

async function mutationResult<T>(response: Response): Promise<T> {
  if (!response.ok) throw await failure(response);
  if (
    response.status === 204 ||
    !response.headers.get("content-type")?.includes("application/json")
  ) {
    return undefined as T;
  }
  return response.json() as Promise<T>;
}

export async function postJSON<T = void>(url: string, body?: unknown): Promise<T> {
  const response = await fetch(url, {
    method: "POST",
    body: body ? JSON.stringify(body) : null,
  });
  return mutationResult<T>(response);
}

export async function putJSON<T = void>(url: string, body: unknown): Promise<T> {
  const response = await fetch(url, { method: "PUT", body: JSON.stringify(body) });
  return mutationResult<T>(response);
}

export async function deleteJSON(url: string): Promise<void> {
  const response = await fetch(url, { method: "DELETE" });
  if (!response.ok) throw await failure(response);
}

export const queryValue = (value: string) => encodeURIComponent(value);
