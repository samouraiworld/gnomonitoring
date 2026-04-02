# Admin Panel Frontend — Handoff Document

Welcome, Zooma. This document explains the Gnomonitoring admin panel, what you're building, and everything you need to get started.

## Overview

The Gnomonitoring admin panel is a web interface for blockchain operators to manage a validator monitoring service without touching config files or databases. It lets admins:

- **Monitor chains** in real time: enable/disable chains, update RPC endpoints, reinitialize data
- **Tune alerts**: configure warning and critical thresholds for missed blocks
- **Review alerts**: filter and inspect historical alerts by chain, level, and date
- **Manage validators**: override validator display names (monikers)
- **Manage users**: view registered users and revoke access (cascades to webhooks)
- **Manage webhooks**: review and delete Discord/Slack validator and GovDAO webhooks
- **Configure Telegram**: manage chat registrations, validator subscriptions, and report schedules
- **Configure web reports**: set daily report schedules for web-authenticated users
- **Monitor proposals**: view active GovDAO proposals being tracked

The backend is **fully implemented**. All API routes exist, are tested, and ready to use. Your job is to build the frontend React UI.

---

## Repository Structure

The project is a monorepo:

```
gnomonitoring/
├── backend/                    # Go backend (API server on :8989)
│   ├── internal/api/
│   │   └── api-admin.go       # All admin routes implemented
│   ├── internal/database/
│   │   └── db_admin.go        # DB queries for admin endpoints
│   └── main.go
├── panel/                      # Your work — React frontend (you create this)
│   ├── package.json           # Vite + React dependencies
│   ├── vite.config.ts         # Vite proxy configuration
│   ├── src/
│   │   ├── main.tsx
│   │   ├── App.tsx
│   │   └── pages/             # One file per page/feature
│   └── HANDOFF.md             # This file
└── ...
```

The frontend is served by Vite in development, static files in production. The backend is a separate Go service running on `:8989`.

---

## Tech Stack

**Required:**
- **Vite** — fast dev server and build tool
- **React 18+** — frontend framework (TypeScript preferred but not mandatory)
- **TypeScript** — optional but recommended for API contract confidence

**UI library:**
No specific UI library is mandated. Choose what fits your style:
- **shadcn/ui** — headless, Tailwind-based, very clean
- **Chakra UI** — accessible, batteries-included
- **Material-UI** — comprehensive but heavier
- Or roll your own with plain CSS / Tailwind

**HTTP client:**
- **fetch API** — built-in, sufficient for this use case
- **axios** / **TanStack Query** — optional if you want advanced features

**Routing:**
- **React Router v6+** — standard choice

**Build & tooling:**
- Vite (config provided)
- ESLint + Prettier (optional but recommended)

---

## Authentication

### Production: Clerk JWT

In production, the admin panel is protected by Clerk authentication:

1. User logs in via Clerk (Google, email, or SAML)
2. Clerk issues a JWT token
3. Frontend stores the token in **sessionStorage** (NOT localStorage, for security)
4. Every API request includes the header: `Authorization: Bearer <token>`
5. Backend validates the token and checks `user.publicMetadata.role == "admin"`
6. Non-admin users get **403 Forbidden**

The frontend typically lives on the same domain as an existing Clerk-powered app. You can reuse Clerk integration from the main site, or set up a new Clerk application if needed.

### Development: Dev Mode

During local development, set `dev_mode: true` in `backend/config.yaml`:

```yaml
dev_mode: true
backend_port: "8989"
```

In dev mode:
- **No authentication is required**
- You can call the API directly from Vite without any auth header
- Clerk SDK is still loaded but auth checks are skipped on the backend

This is ideal for rapid frontend iteration without Clerk setup. When ready to test real auth, flip `dev_mode: false`.

### Vite Proxy for CORS

Configure Vite to proxy `/admin/*` requests to the backend. This avoids CORS issues in development:

```typescript
// vite.config.ts
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/admin': {
        target: 'http://localhost:8989',
        changeOrigin: true,
        rewrite: (path) => path,
      },
    },
  },
});
```

With this proxy:
- Vite dev server runs on `http://localhost:5173`
- Backend runs on `http://localhost:8989`
- Fetch to `/admin/chains` from the frontend goes to `http://localhost:8989/admin/chains` via the proxy
- No CORS errors

In production, the frontend and backend are served from the same origin, so the proxy is not needed.

---

## API Reference

All endpoints are under `/admin/` on the backend (`:8989`).

### Error Handling Convention

**Success (200/201):**
- HTTP status 200 or 201
- Response body is JSON

**Client error (400):**
- HTTP status 400
- Response body is plain text error message

**Unauthorized (401):**
- HTTP status 401
- Response body: "Unauthorized"

**Forbidden (403):**
- HTTP status 403
- Response body: "Forbidden"

**Not found (404):**
- HTTP status 404
- Response body: "not found"

**Server error (500):**
- HTTP status 500
- Response body is plain text error message

**Always check the HTTP status code first.** Parse JSON only if status is 2xx.

---

### 1. Status Overview

#### `GET /admin/status`

Returns a live snapshot of all chains and global metrics.

**Response (200):**
```json
{
  "chains": [
    {
      "chain_id": "betanet",
      "height": 1234567,
      "active_alerts": 2,
      "critical_alerts": 1,
      "goroutine_active": true
    },
    {
      "chain_id": "gnoland1",
      "height": 5678901,
      "active_alerts": 0,
      "critical_alerts": 0,
      "goroutine_active": false
    }
  ],
  "total_users": 42,
  "total_webhooks": 18,
  "total_telegram_chats": 7
}
```

**Use case:** Dashboard page. Show per-chain cards and total counts.

---

### 2. Chain Management

#### `GET /admin/chains`

List all configured chains with current status.

**Response (200):**
```json
[
  {
    "id": "betanet",
    "rpc_endpoint": "https://rpc.betanet.gno.land",
    "graphql": "https://indexer.betanet.gno.land/graphql/query",
    "gnoweb": "https://betanet.gno.land",
    "enabled": true,
    "goroutine_active": true,
    "height": 1234567
  }
]
```

---

#### `POST /admin/chains`

Add a new chain to the system.

**Request:**
```json
{
  "id": "newchain",
  "rpc_endpoint": "https://rpc.newchain.example.com",
  "graphql": "https://indexer.newchain.example.com/graphql/query",
  "gnoweb": "https://newchain.example.com",
  "enabled": true
}
```

**Response (201):**
```json
{
  "status": "created",
  "id": "newchain"
}
```

**Notes:**
- `id` and `rpc_endpoint` are required
- `graphql` and `gnoweb` are optional (can be empty strings)
- If `enabled: true`, the backend starts monitoring goroutines immediately
- No RPC endpoint validation — the backend accepts any URL; errors appear in logs if unreachable

---

#### `PUT /admin/chains/:id`

Update a chain's configuration (partial update, all fields optional).

**Request:**
```json
{
  "rpc_endpoint": "https://new-rpc.example.com",
  "graphql": "...",
  "gnoweb": "...",
  "enabled": false
}
```

**Response (200):**
```json
{
  "status": "updated",
  "id": "betanet"
}
```

**Notes:**
- Omit fields you don't want to change
- Setting `enabled: false` stops the monitoring goroutine
- Setting `enabled: true` starts it if not already running

---

#### `DELETE /admin/chains/:id`

**Irreversible:** Stops monitoring, removes from config, deletes ALL chain data from the database (participations, alerts, monikers, Telegram subscriptions).

**Response (200):**
```json
{
  "status": "deleted",
  "id": "betanet"
}
```

**Important:** Show a strong confirmation modal before sending this request.

---

#### `POST /admin/chains/:id/reinit`

Purge participation data only (daily_participations, daily_participation_agregas, alert_logs). Keeps monikers and Telegram subscriptions.

**Response (200):**
```json
{
  "status": "reinitialized",
  "id": "betanet"
}
```

---

#### `POST /admin/config/reload`

Hot-reload `config.yaml` from disk into memory without restarting the backend process.

**Response (200):**
```json
{
  "status": "reloaded"
}
```

---

### 3. Alert Thresholds

#### `GET /admin/config/thresholds`

Get all alert configuration keys and values.

**Response (200):**
```json
{
  "warning_threshold": "5",
  "critical_threshold": "30",
  "mute_after_n_alerts": "1",
  "mute_duration_minutes": "60",
  "resolve_mute_after_n": "4",
  "stagnation_first_alert_seconds": "20",
  "stagnation_repeat_minutes": "30",
  "rpc_error_cooldown_minutes": "10",
  "new_validator_scan_minutes": "5",
  "alert_check_interval_seconds": "20",
  "raw_retention_days": "7",
  "aggregator_period_minutes": "60"
}
```

**Important:** All values are returned as **strings**. Cast to `int` in the form validation and when displaying.

---

#### `PUT /admin/config/thresholds`

Update one or more threshold values.

**Request (partial):**
```json
{
  "warning_threshold": "10",
  "critical_threshold": "50"
}
```

**Response (200):**
```json
{
  "status": "updated"
}
```

**Notes:**
- Only send changed fields
- Backend validates and refreshes in-memory cache immediately
- All values must be valid integers (backend will reject invalid formats)

---

### 4. Alert History

#### `GET /admin/alerts`

Query alert logs with optional filters.

**Query parameters:**
- `?chain=<id>` — filter by chain (optional)
- `?level=WARNING|CRITICAL` — filter by severity (optional)
- `?from=<ISO-date>` — start date (optional, e.g., `2026-04-01T00:00:00Z`)
- `?to=<ISO-date>` — end date (optional)
- `?limit=<int>` — default 200, max reasonable is 1000 (optional)

**Response (200):**
```json
[
  {
    "ID": 1,
    "chain_id": "betanet",
    "addr": "g1abc123...",
    "moniker": "MyValidator",
    "level": "WARNING",
    "start_height": 12345,
    "end_height": 12350,
    "skipped": false,
    "msg": "Validator missed 5 consecutive blocks",
    "sent_at": "2026-04-01T10:00:00Z"
  }
]
```

**Use case:** Alert history page. Provide filters for chain, level, and date range.

---

#### `DELETE /admin/alerts?chain=<id>`

Purge all alert logs for a specific chain.

**Query parameter:** `?chain=` is **required**

**Response (200):**
```json
{
  "status": "purged",
  "chain": "betanet"
}
```

**Important:** Show a strong confirmation modal.

---

### 5. Monikers (Validator Display Names)

#### `GET /admin/monikers`

List all moniker overrides, optionally filtered by chain.

**Query parameter:**
- `?chain=<id>` — filter by chain (optional)

**Response (200):**
```json
[
  {
    "ID": 1,
    "chain_id": "betanet",
    "addr": "g1abc123...",
    "moniker": "MyValidator",
    "first_active_block": 1000
  }
]
```

---

#### `POST /admin/monikers`

Insert or update (upsert) a moniker override.

**Request:**
```json
{
  "chain_id": "betanet",
  "addr": "g1abc123...",
  "moniker": "CustomValidatorName"
}
```

**Response (201):**
```json
{
  "status": "created"
}
```

---

#### `PUT /admin/monikers/:chain/:addr`

Update the moniker name for a specific (chain, addr) pair.

**Request:**
```json
{
  "moniker": "NewName"
}
```

**Response (200):**
```json
{
  "status": "updated"
}
```

---

#### `DELETE /admin/monikers/:chain/:addr`

Delete a moniker override. The validator will fall back to the on-chain name.

**Response (200):**
```json
{
  "status": "deleted"
}
```

---

### 6. User Management

#### `GET /admin/users`

List all registered web users.

**Response (200):**
```json
[
  {
    "user_id": "user_2abc...",
    "name": "John Doe",
    "email": "john@example.com",
    "created_at": "2026-01-15T08:30:00Z"
  }
]
```

---

#### `DELETE /admin/users/:id`

Delete a user and cascade: webhooks (validator + govdao), alert contacts, hour reports.

**Response (200):**
```json
{
  "status": "deleted",
  "user_id": "user_2abc..."
}
```

**Important:** Warn the user that all related data (webhooks, contacts, schedules) is deleted too.

---

### 7. Webhook Management

#### `GET /admin/webhooks`

List all webhooks (validator and govdao) across all users.

**Response (200):**
```json
[
  {
    "id": 1,
    "user_id": "user_2abc...",
    "url": "https://discord.com/api/webhooks/...",
    "type": "discord",
    "description": "My Discord channel",
    "chain_id": "betanet",
    "kind": "validator"
  },
  {
    "id": 2,
    "user_id": "user_2abc...",
    "url": "https://hooks.slack.com/...",
    "type": "slack",
    "description": "Governance alerts",
    "chain_id": null,
    "kind": "govdao"
  }
]
```

**Note:** `kind` is either `"validator"` or `"govdao"`. `chain_id` may be null for some webhook types.

---

#### `DELETE /admin/webhooks/:type/:id`

Delete a webhook by type and ID.

**Path parameters:**
- `:type` — `validator` or `govdao`
- `:id` — integer ID

**Response (200):**
```json
{
  "status": "deleted"
}
```

---

#### `PUT /admin/webhooks/govdao/:id/reset`

Reset the tracker for a stuck GovDAO webhook (sets `last_checked_id` to `-1`).

**Response (200):**
```json
{
  "status": "reset"
}
```

**Use case:** If a GovDAO proposal tracker stops receiving updates, click "Reset tracker" to unblock it.

---

### 8. Telegram Management

#### `GET /admin/telegram/chats`

List all registered Telegram chats.

**Response (200):**
```json
[
  {
    "chat_id": 123456789,
    "type": "validator",
    "chain_id": "betanet"
  }
]
```

---

#### `DELETE /admin/telegram/chats/:id`

Deregister a Telegram chat and cascade: delete telegram_hour_reports and telegram_validator_subs.

**Response (200):**
```json
{
  "status": "deleted"
}
```

---

#### `GET /admin/telegram/subs`

List Telegram validator subscriptions, optionally filtered by chain and/or chat.

**Query parameters (both optional):**
- `?chain=<id>`
- `?chat_id=<int>`

**Response (200):**
```json
[
  {
    "ID": 1,
    "chat_id": 123456789,
    "chain_id": "betanet",
    "moniker": "MyValidator",
    "addr": "g1abc123...",
    "activate": true,
    "created_at": "2026-01-01T00:00:00Z"
  }
]
```

---

#### `PUT /admin/telegram/subs/:id`

Toggle a subscription's active state.

**Request:**
```json
{
  "activate": false
}
```

**Response (200):**
```json
{
  "status": "updated"
}
```

---

#### `GET /admin/telegram/schedules`

List all Telegram daily report schedules.

**Response (200):**
```json
[
  {
    "chat_id": 123456789,
    "chain_id": "betanet",
    "daily_report_hour": 9,
    "daily_report_minute": 0,
    "activate": true,
    "timezone": "Europe/Paris"
  }
]
```

---

#### `PUT /admin/telegram/schedules/:chat_id/:chain`

Update a Telegram report schedule.

**Request:**
```json
{
  "hour": 8,
  "minute": 30,
  "timezone": "UTC",
  "activate": true
}
```

**Response (200):**
```json
{
  "status": "updated"
}
```

---

### 9. Web User Report Schedules

#### `GET /admin/schedules`

List all web user report schedules.

**Response (200):**
```json
[
  {
    "user_id": "user_2abc...",
    "daily_report_hour": 9,
    "daily_report_minute": 0,
    "timezone": "Europe/Paris"
  }
]
```

---

#### `PUT /admin/schedules/:user_id`

Update a web user's report schedule.

**Request:**
```json
{
  "hour": 7,
  "minute": 0,
  "timezone": "America/New_York"
}
```

**Response (200):**
```json
{
  "status": "updated"
}
```

---

### 10. GovDAO Proposals

#### `GET /admin/govdao/proposals`

List tracked GovDAO proposals for a chain.

**Query parameter:**
- `?chain=<id>` — filter by chain (defaults to default chain if omitted)

**Response (200):**
```json
[
  {
    "Id": 42,
    "ChainID": "betanet",
    "Url": "https://betanet.gno.land/r/gov/dao:42",
    "Title": "Increase validator commission",
    "Tx": "0xabc123...",
    "Status": "active"
  }
]
```

---

## Page-by-Page UI Specification

Build these pages/sections. Use the API endpoints above as your data source.

### Dashboard (`/`)

**Purpose:** Real-time system overview.

**Components:**
- **Per-chain status cards:** Show chain ID, current block height, goroutine active indicator (green/red), active alert count (warning + critical), and total alerts badge
- **Global stats:** Total registered users, total webhooks (validator + govdao), total Telegram chats
- **Recent alerts table:** Last 10 alerts (call `GET /admin/alerts?limit=10`), columns: sent_at, chain, moniker, level, start_height
- **Action button:** Link to full alert history page

**Load on mount:** Call `GET /admin/status` and `GET /admin/alerts?limit=10`. Consider auto-refreshing every 10 seconds for real-time feel (optional).

---

### Chain Management (`/chains`)

**Purpose:** Enable/disable chains, update endpoints, reinitialize data.

**Components:**
- **Add chain form:** Modal or inline form with fields: chain ID, RPC endpoint, GraphQL endpoint, GnoWeb endpoint. Toggle "Enable monitoring" on/off. Submit button calls `POST /admin/chains`.
- **Chains table:** Columns: ID, RPC endpoint (truncated), GraphQL endpoint (truncated), GnoWeb endpoint (truncated), enabled toggle, goroutine active indicator, current height, actions
- **Enable/disable toggle:** Calls `PUT /admin/chains/:id` with `{ "enabled": true/false }`
- **Edit button:** Opens form to update RPC, GraphQL, GnoWeb endpoints. Calls `PUT /admin/chains/:id`
- **Delete button:** Confirmation modal (red warning: "All chain data will be deleted. This is irreversible.") → `DELETE /admin/chains/:id`
- **Reinit button:** Confirmation modal (yellow warning: "Reinitialization will clear all participation history but keep monikers and Telegram subscriptions.") → `POST /admin/chains/:id/reinit`
- **Config reload button:** Calls `POST /admin/config/reload`. Show success/error toast.

**Load on mount:** Call `GET /admin/chains`.

---

### Alert Configuration (`/config`)

**Purpose:** Tune alert thresholds.

**Components:**
- **Form with 12 fields:** One field for each key from `GET /admin/config/thresholds`
  - Convert key names to labels: replace underscores with spaces, capitalize each word
  - Example: `"warning_threshold"` → "Warning Threshold"
  - Use number inputs with validation (must be positive integer)
- **Save button:** Calls `PUT /admin/config/thresholds` with only changed fields
- **Reset button (optional):** Reload from API to discard unsaved changes

**Load on mount:** Call `GET /admin/config/thresholds`. Display all values.

**Validation:**
- All values must be non-negative integers
- Block save if any field is invalid

---

### Alert History (`/alerts`)

**Purpose:** Review all alerts sent.

**Components:**
- **Filters:**
  - Chain dropdown (from `GET /admin/chains`)
  - Level dropdown: "All", "WARNING", "CRITICAL"
  - Date range picker: "From" and "To" (ISO date format)
  - Limit slider or number input (default 200, max 1000)
  - "Apply filters" button → call `GET /admin/alerts?chain=...&level=...&from=...&to=...&limit=...`
- **Alerts table:** Columns: sent_at, chain, moniker, addr (truncated), level (color-coded: orange=WARNING, red=CRITICAL), start_height, end_height, skipped (boolean), msg (truncated, hover for full text)
- **Purge by chain button:** Dropdown to select chain → confirmation modal → `DELETE /admin/alerts?chain=...`

**Load on mount:** Call `GET /admin/alerts?limit=200` (no filters).

---

### Moniker Management (`/monikers`)

**Purpose:** Override validator display names.

**Components:**
- **Chain filter dropdown:** Filter the table by selected chain
- **Monikers table:** Columns: chain, addr (truncated, copy button), moniker, first_active_block
- **Inline edit:** Click on moniker cell → editable input field → on blur or Enter, call `PUT /admin/monikers/:chain/:addr` with `{ "moniker": "NewValue" }`
- **Delete button per row:** Calls `DELETE /admin/monikers/:chain/:addr`
- **Add moniker form:** Modal with fields: chain (dropdown), addr, moniker. Submit → `POST /admin/monikers`

**Load on mount:** Call `GET /admin/monikers` (no filter). When chain filter changes, re-fetch with `?chain=...`.

---

### User Management (`/users`)

**Purpose:** View and revoke users.

**Components:**
- **Users table:** Columns: user_id, name, email, created_at (formatted as readable date)
- **Delete button per user:** Confirmation modal (red warning: "Deleting a user also deletes their webhooks, alert contacts, and report schedules.") → `DELETE /admin/users/:id`

**Load on mount:** Call `GET /admin/users`.

---

### Webhook Management (`/webhooks`)

**Purpose:** Review and delete webhooks.

**Components:**
- **Two tabs: "Validator Webhooks" and "GovDAO Webhooks"** (filter by `kind`)
- **Webhooks table (per tab):** Columns: id, user_id, description, type (discord/slack/etc.), chain (if applicable), url (truncated, copy button), actions
- **Delete button per webhook:** `DELETE /admin/webhooks/:kind/:id`
- **Reset tracker button (GovDAO webhooks only):** `PUT /admin/webhooks/govdao/:id/reset`. Show success toast.

**Load on mount:** Call `GET /admin/webhooks`. Filter client-side by `kind` for the two tabs.

---

### Telegram Management (`/telegram`)

Three tabs: **Chats**, **Subscriptions**, **Schedules**

#### Tab 1: Chats
- **Chats table:** Columns: chat_id, type, chain_id
- **Delete button per chat:** `DELETE /admin/telegram/chats/:id`

#### Tab 2: Subscriptions
- **Filters:** Chain dropdown, Chat ID text input (optional)
- **Subscriptions table:** Columns: chat_id, chain, moniker, addr (truncated), active (toggle switch)
- **Toggle switch per row:** Calls `PUT /admin/telegram/subs/:id` with `{ "activate": true/false }`

#### Tab 3: Schedules
- **Schedules table:** Columns: chat_id, chain, hour:minute (e.g., "09:00"), timezone, active (toggle switch)
- **Inline edit:** Click on hour/minute/timezone cell → open time picker / timezone selector → on save, call `PUT /admin/telegram/schedules/:chat_id/:chain`
- **Toggle active:** `PUT /admin/telegram/schedules/:chat_id/:chain` with `{ "activate": true/false }`

**Load on mount:** Call all three telegram endpoints (`GET /admin/telegram/chats`, `GET /admin/telegram/subs`, `GET /admin/telegram/schedules`).

---

### Web Schedules (`/schedules`)

**Purpose:** Configure daily report times for web users.

**Components:**
- **Schedules table:** Columns: user_id, hour:minute (e.g., "09:00"), timezone
- **Inline edit:** Click on hour/minute/timezone cell → open time picker / timezone selector → on save, call `PUT /admin/schedules/:user_id`

**Load on mount:** Call `GET /admin/schedules`.

---

### GovDAO Proposals (`/govdao`)

**Purpose:** View proposals being tracked.

**Components:**
- **Chain selector dropdown:** Switch between enabled chains
- **Proposals table:** Columns: id, title, status, tx (truncated, copy button), url (linked to gnoweb, opens in new tab)
- **Auto-refresh (optional):** Every 30 seconds

**Load on mount:** Call `GET /admin/govdao/proposals?chain=<default>`. When chain changes, re-fetch with new chain.

---

## Recommended Component Structure

Below is a suggested folder structure to keep code organized:

```
panel/src/
├── main.tsx                    # Vite entry point
├── App.tsx                     # Router setup
├── hooks/
│   ├── useAuth.ts             # Clerk auth, token management
│   ├── useApi.ts              # Fetch wrapper with error handling
│   └── useTitle.ts            # Page title management
├── pages/
│   ├── Dashboard.tsx
│   ├── Chains.tsx
│   ├── AlertConfig.tsx
│   ├── AlertHistory.tsx
│   ├── Monikers.tsx
│   ├── Users.tsx
│   ├── Webhooks.tsx
│   ├── Telegram.tsx
│   ├── Schedules.tsx
│   ├── GovDAO.tsx
│   └── Login.tsx
├── components/
│   ├── Header.tsx             # Navigation bar
│   ├── Sidebar.tsx            # Menu
│   ├── LoadingSpinner.tsx
│   ├── ConfirmModal.tsx
│   ├── ErrorAlert.tsx
│   ├── SuccessToast.tsx
│   └── ...
├── types/
│   └── api.ts                 # TypeScript interfaces for API responses
├── utils/
│   ├── api.ts                 # API request helpers
│   └── format.ts              # Formatting utilities (dates, addresses)
└── styles/
    └── globals.css            # Global styles
```

---

## Key Implementation Notes

### 1. Global Error Handling

Create a custom hook for API calls that standardizes error handling:

```typescript
// hooks/useApi.ts
const useApi = () => {
  const navigate = useNavigate();
  const { token } = useAuth();

  const call = async (
    method: string,
    path: string,
    body?: object
  ): Promise<any> => {
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
    };
    if (token) {
      headers['Authorization'] = `Bearer ${token}`;
    }

    const response = await fetch(`/admin${path}`, {
      method,
      headers,
      body: body ? JSON.stringify(body) : undefined,
    });

    if (response.status === 401 || response.status === 403) {
      navigate('/login');
      throw new Error('Unauthorized');
    }

    if (!response.ok) {
      const text = await response.text();
      throw new Error(text || `HTTP ${response.status}`);
    }

    if (response.status === 204 || response.headers.get('content-length') === '0') {
      return null;
    }

    return response.json();
  };

  return { call };
};
```

### 2. Confirmation Modals

Always show a confirmation modal before destructive actions:
- DELETE /admin/chains/:id
- DELETE /admin/alerts?chain=...
- DELETE /admin/users/:id
- DELETE /admin/telegram/chats/:id

Use a reusable `ConfirmModal` component:

```typescript
<ConfirmModal
  isOpen={showConfirm}
  title="Delete chain?"
  message="All data for this chain will be permanently deleted. This cannot be undone."
  confirmText="Delete"
  cancelText="Cancel"
  isDestructive
  onConfirm={handleDelete}
  onCancel={() => setShowConfirm(false)}
/>
```

### 3. Timezone Support

For Telegram and Web schedules, let users pick from standard IANA timezones. Use a library like `moment-timezone` or just hardcode common zones:

```typescript
const TIMEZONES = [
  'UTC',
  'Europe/Paris',
  'Europe/London',
  'America/New_York',
  'America/Los_Angeles',
  'Asia/Tokyo',
];
```

### 4. Form Validation

For alert configuration, validate that all threshold values are:
- Non-empty
- Valid integers
- Non-negative

For moniker addition, validate:
- Chain is selected
- Address is non-empty
- Moniker is non-empty

### 5. Copy-to-Clipboard Buttons

For long fields (addresses, URLs, transaction hashes), add a copy button:

```typescript
const copyToClipboard = (text: string) => {
  navigator.clipboard.writeText(text);
  // Show brief toast "Copied!"
};
```

### 6. Date Formatting

Use a library like `date-fns` for consistent date formatting:

```typescript
import { format } from 'date-fns';

format(new Date(alert.sent_at), 'yyyy-MM-dd HH:mm:ss');
```

### 7. Address Truncation

Show full address on hover:

```typescript
const truncateAddress = (addr: string, chars = 10) => {
  if (addr.length <= chars * 2) return addr;
  return `${addr.slice(0, chars)}...${addr.slice(-chars)}`;
};
```

---

## Dev Workflow

### 1. Initial Setup

```bash
cd panel
npm install
```

### 2. Start Dev Server

From the `panel/` directory:

```bash
npm run dev
```

Vite will start on `http://localhost:5173` by default.

### 3. Start Backend

From the `backend/` directory (in a separate terminal):

```bash
# Ensure dev_mode: true in config.yaml
go run main.go
```

Backend runs on `http://localhost:8989`.

### 4. Visit the App

Open `http://localhost:5173` in your browser. The Vite proxy will forward `/admin/*` requests to the backend.

### 5. Test an API Call

In the browser console:

```javascript
fetch('/admin/status').then(r => r.json()).then(console.log);
```

You should see the status JSON.

---

## Testing Checklist

Before declaring the frontend complete, test:

- [ ] All pages load without errors
- [ ] Auth flow works (login page shows, JWT is attached to requests)
- [ ] Dashboard: status loads, auto-refreshes (if implemented)
- [ ] Chains: list, add, edit, delete, reinit, toggle enable (show confirmation dialogs)
- [ ] Config: load all thresholds, edit, save (show validation errors for invalid inputs)
- [ ] Alerts: list, filter by chain/level/date, purge (show confirmation dialog)
- [ ] Monikers: list, filter by chain, add, inline edit, delete
- [ ] Users: list, delete (show confirmation dialog)
- [ ] Webhooks: list both kinds, delete (show confirmation dialog), reset tracker
- [ ] Telegram: list chats, subs, schedules; toggle subs; edit schedules
- [ ] Web Schedules: list, edit (time picker works, timezone selector works)
- [ ] GovDAO: list proposals, chain selector switches data
- [ ] Error handling: network error shows user-friendly message, 403 redirects to login
- [ ] Responsive: looks good on desktop and tablet (mobile is nice-to-have)

---

## Known Limitations

1. **All threshold values are strings from the API.** Cast to `int` in the form. Validate inputs as integers before sending.

2. **DELETE /admin/chains/:id is irreversible.** Always show a prominent warning modal.

3. **Chain addition does not validate RPC endpoint reachability.** The backend accepts any URL. Bad endpoints will only error during first use.

4. **Web report schedules always affect the default chain only.** This is a backend limitation, not a UI issue. Document this if you add inline help.

5. **Goroutine active indicator** (`goroutine_active: true`) means the goroutine is registered, not necessarily that the chain is healthy. A goroutine can be running but failing to fetch blocks.

6. **No pagination** on most endpoints. Alerts default to limit 200. Add a UI control to let users set the limit for larger deployments.

7. **Vite proxy** only works in dev mode. In production, ensure the frontend and backend are served from the same origin (same hostname).

8. **Clerk integration** requires setting up a Clerk application and configuring the frontend SDK. This is outside the scope of this handoff but see Clerk docs.

9. **Session Storage vs. Local Storage:** Store JWT in sessionStorage to prevent persistence across browser close. This is more secure for admin tokens.

---

## Questions?

Refer back to:
- **API behavior:** `backend/internal/api/api-admin.go` in the repo
- **Data models:** `backend/internal/database/db_*.go` for field names and types
- **Project architecture:** `CLAUDE.md` at the repo root

Good luck! Build something solid and maintainable. The backend is ready to support you.
