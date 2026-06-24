// data/client.js — the single place that knows the native /v1 API.
//
// Every view and the graph fetch through here, so URL shape and HTTP-status
// semantics live in exactly one module. get() throws ApiError carrying the
// status so callers can special-case 404/503; tryGet() turns those into null
// for optional fragments; logs() returns the bare-array log shape.

const BASE = '/v1';

// ApiError carries the HTTP status of a non-2xx response so callers can branch
// on 404 (absent) vs 503 (subsystem unavailable) without re-reading the body.
export class ApiError extends Error {
  constructor(status, path) {
    super('GET ' + path + ' -> ' + status);
    this.name = 'ApiError';
    this.status = status;
    this.path = path;
  }
}

// get fetches and parses JSON from a /v1 path (e.g. "/loops" or
// "/requests/abc"). Throws ApiError on any non-2xx response; AbortError
// propagates from a passed AbortSignal.
export async function get(path, { signal } = {}) {
  const resp = await fetch(BASE + path, { signal });
  if (!resp.ok) throw new ApiError(resp.status, path);
  return resp.json();
}

// tryGet is get() that returns null instead of throwing on 404/503, for
// fragments that may legitimately be absent (an unconfigured router, an empty
// registry). Other errors still throw.
export async function tryGet(path, opts) {
  try {
    return await get(path, opts);
  } catch (err) {
    if (err instanceof ApiError && (err.status === 404 || err.status === 503)) {
      return null;
    }
    throw err;
  }
}

// logs fetches a bare-array log response (the /v1/*/logs convention) and always
// returns an array, empty on failure.
export async function logs(path, opts) {
  try {
    const data = await get(path, opts);
    return Array.isArray(data) ? data : [];
  } catch (err) {
    if (err.name === 'AbortError') throw err;
    return [];
  }
}
