# Feat: Fallback URLs for RPC / GraphQL / GnoWeb endpoints

## Problem

Each chain currently has a single URL for `rpc_endpoint`, `graphql`, and `gnoweb`.
If the primary URL is down or unresponsive, the monitoring loop, GovDAO watcher, and alerts all fail until the config is manually edited and the backend restarted.

## Goal

Support a list of fallback URLs per endpoint type. When the active URL fails, the system automatically tries the next one in the list — transparently, without restart.

## Backward compatibility constraint

Existing configs with a single string (`rpc_endpoint: "https://..."`) must continue to work unchanged.
New configs can use either form:

```yaml
chains:
  test12:
    rpc_endpoint: "https://rpc.primary.example.com"          # single string — still valid
    # or:
    rpc_endpoints:
      - "https://rpc.primary.example.com"
      - "https://rpc.fallback.example.com"
    graphql: "https://indexer.primary.example.com/graphql/query"
    graphqls:
      - "https://indexer.primary.example.com/graphql/query"
      - "https://indexer.fallback.example.com/graphql/query"
    gnoweb: "https://primary.gno.land"
    gnowebs:
      - "https://primary.gno.land"
      - "https://fallback.gno.land"
    enabled: true
```

Rule: plural form (`rpc_endpoints`) takes precedence. If absent, the singular field is wrapped into a one-element slice at load time.

---

## Architecture decision

- **RPC**: `rpcclient.NewHTTPClient` creates a client bound to a single URL. We need a thin wrapper `FallbackRPCClient` that holds the slice, tracks the active index, and rotates on error.
- **GraphQL**: calls are plain HTTP requests (in `govdao.go`). A helper `tryGraphQLWithFallback(endpoints []string, query string) ([]byte, error)` replaces direct calls.
- **GnoWeb**: URLs are used only to construct clickable links in messages — no actual HTTP call is made. The "active" URL is the first in the list; no runtime fallback needed (order = priority).

---

## Step-by-step plan

### Step 1 — YAML custom unmarshaling in `internal/fonction.go`

**File:** `backend/internal/fonction.go`

Change `ChainConfig`:

```go
type ChainConfig struct {
    RPCEndpoints     []string `yaml:"rpc_endpoints"`
    GraphqlEndpoints []string `yaml:"graphqls"`
    GnowebEndpoints  []string `yaml:"gnowebs"`
    Enabled          bool     `yaml:"enabled"`
    // legacy single-URL fields — read-only at load time, never written back
    rpcEndpointLegacy     string `yaml:"rpc_endpoint"`
    graphqlEndpointLegacy string `yaml:"graphql"`
    gnowebEndpointLegacy  string `yaml:"gnoweb"`
}
```

Add a custom `UnmarshalYAML` on `ChainConfig` that:
1. Unmarshals into a raw struct containing both the plural and singular fields.
2. If the plural slice is non-empty, uses it directly.
3. Otherwise wraps the singular string in a one-element slice.
4. Result is always `RPCEndpoints []string` with at least one element (or error if both are empty).

Add convenience accessor methods (used everywhere instead of direct field access):

```go
func (c *ChainConfig) RPCEndpoint() string     { return c.RPCEndpoints[0] }
func (c *ChainConfig) GraphqlEndpoint() string { return c.GraphqlEndpoints[0] }
func (c *ChainConfig) GnowebEndpoint() string  { return c.GnowebEndpoints[0] }
```

These make the rest of the codebase changes minimal: most call sites only need the primary URL, and only the places that actually create connections need to be updated.

Update `config_test.go` to cover both single-string and list YAML forms.

> Note: the existing field names `RPCEndpoint string`, `GraphqlEndpoint string`, `GnowebEndpoint string` become methods rather than fields. All call sites that currently read `cfg.RPCEndpoint` become `cfg.RPCEndpoint()` — a mechanical rename.

---

### Step 2 — Rename all field accesses to method calls

A mechanical find-and-replace across the codebase:

| Old (field) | New (method call) |
|---|---|
| `chainCfg.RPCEndpoint` | `chainCfg.RPCEndpoint()` |
| `chainCfg.GraphqlEndpoint` | `chainCfg.GraphqlEndpoint()` |
| `chainCfg.GnowebEndpoint` | `chainCfg.GnowebEndpoint()` |
| `cfg.RPCEndpoint` | `cfg.RPCEndpoint()` |

Affected files (from current grep):
- `backend/main.go`
- `backend/internal/api/api.go`
- `backend/internal/api/api-admin.go`
- `backend/internal/gnovalidator/gnovalidator_realtime.go`
- `backend/internal/govdao/govdao.go`

The admin panel POST/PUT endpoints that accept `rpc_endpoint` as a JSON string also need to be updated to accept an array OR a single string (mirror the YAML approach).

---

### Step 3 — `FallbackRPCClient` wrapper in a new file `gnovalidator/rpc_fallback.go`

**File:** `backend/internal/gnovalidator/rpc_fallback.go` (new file)

```go
type FallbackRPCClient struct {
    endpoints []string
    activeIdx int
    mu        sync.Mutex
}

// Block, Status, ABCIQuery, etc. — each method:
// 1. Tries current activeIdx.
// 2. On any network/timeout error, increments activeIdx (mod len), recreates the inner client, retries once.
// 3. Returns error only if all endpoints fail.
```

This struct satisfies the same interface used by `gnoclient.Client` (i.e., it wraps `rpcclient.HTTPClient`).

In `StartValidatorMonitoring` (`gnovalidator_realtime.go`), replace:

```go
rpcClient, err := rpcclient.NewHTTPClient(chainCfg.RPCEndpoint())
client := gnoclient.Client{RPCClient: rpcClient}
```

with:

```go
rpcClient := NewFallbackRPCClient(chainCfg.RPCEndpoints)
client := gnoclient.Client{RPCClient: rpcClient}
```

Log a warning each time a fallback occurs: `[rpc][chainID] primary URL failed, switching to fallback #N`.

---

### Step 4 — GraphQL fallback helper in `govdao/govdao.go`

**File:** `backend/internal/govdao/govdao.go`

Add a package-local helper:

```go
func doGraphQLRequest(endpoints []string, body []byte) ([]byte, error) {
    for i, url := range endpoints {
        resp, err := http.Post(url, "application/json", bytes.NewReader(body))
        if err == nil && resp.StatusCode < 500 {
            return io.ReadAll(resp.Body)
        }
        log.Printf("[graphql] endpoint #%d failed (%s): %v, trying next", i, url, err)
    }
    return nil, fmt.Errorf("all %d GraphQL endpoints failed", len(endpoints))
}
```

Replace every direct HTTP call to `GraphqlEndpoint` with a call to `doGraphQLRequest(chainCfg.GraphqlEndpoints, ...)`.

`StartGovDAo` signature changes from `(graphql, rpc, gnoweb string)` to accepting `chainCfg *internal.ChainConfig` directly — cleaner and avoids re-threading the slices as individual args.

---

### Step 5 — GnoWeb: priority list, no runtime fallback

GnoWeb URLs are only used to construct links (no actual HTTP call in the hot path). No fallback client needed.

`chainCfg.GnowebEndpoint()` returns `GnowebEndpoints[0]` — the highest-priority URL. This is already covered by the accessor added in Step 1.

If desired in the future, a health-check at startup could probe the list and promote the first responding URL, but this is out of scope for this feature.

---

### Step 6 — Admin panel API updates

**File:** `backend/internal/api/api-admin.go`

The GET `/admin/chains` response currently returns:
```json
{ "rpc_endpoint": "...", "graphql": "...", "gnoweb": "..." }
```

Update to:
```json
{
  "rpc_endpoints": ["...", "..."],
  "graphql_endpoints": ["...", "..."],
  "gnoweb_endpoints": ["...", "..."]
}
```

The POST `/admin/chains` (add chain) and PUT `/admin/chains/:id` (update chain) bodies: accept both the singular string (wrapped internally) and the array form.

---

### Step 7 — `config.yaml.template` update

Update the template to document the new multi-URL format with comments, keeping the single-URL form as the default example so existing users are not confused.

---

## Summary of files changed

| File | Change |
|---|---|
| `internal/fonction.go` | `ChainConfig` struct + custom YAML unmarshaling + accessor methods |
| `internal/config_test.go` | Tests for single-string and list YAML forms |
| `internal/gnovalidator/rpc_fallback.go` | New file: `FallbackRPCClient` |
| `internal/gnovalidator/gnovalidator_realtime.go` | Use `FallbackRPCClient`; rename field accesses to method calls |
| `internal/govdao/govdao.go` | `doGraphQLRequest` helper; accept `*ChainConfig` instead of 3 strings |
| `internal/api/api.go` | Rename field accesses; update JSON response struct |
| `internal/api/api-admin.go` | Rename field accesses; update JSON request/response structs |
| `main.go` | Rename field accesses |
| `config.yaml.template` | Document new multi-URL YAML format |

No DB migration needed. No new packages. Fully backward compatible.
