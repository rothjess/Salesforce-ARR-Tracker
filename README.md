# Coder ARR Tracker — Salesforce

Pulls revenue data from Salesforce Opportunities and QuoteLineItems, and displays a live ARR dashboard.

## ARR Methodology

- **Opportunity ARR** is read directly from the `ARR__c` field on each Opportunity
- **Delta ARR** is the sum of `Ruby__DeltaARR__c` across all QuoteLineItems linked to that Opportunity via Quote
- Only `Closed Won` opportunities are included in summary totals

---

## Stack

| Layer     | Tool                                    |
|-----------|-----------------------------------------|
| Backend   | Go 1.21 (stdlib only + lib/pq)          |
| Database  | Supabase (Postgres)                     |
| Frontend  | React + Vite + Recharts                 |
| Auth      | Salesforce OAuth Username-Password flow |

---

## Salesforce Setup

### 1. Create a Connected App

1. In Salesforce, go to **Setup → App Manager → New Connected App**
2. Enable **OAuth Settings**
3. Add the scope: `api`, `refresh_token`
4. Save and note your **Consumer Key** (`SF_CLIENT_ID`) and **Consumer Secret** (`SF_CLIENT_SECRET`)

### 2. Required Fields

| Object         | Field API Name         | Notes                          |
|----------------|------------------------|-------------------------------|
| Opportunity    | `ARR__c`               | Currency field — the deal ARR  |
| QuoteLineItem  | `Ruby__DeltaARR__c`    | Currency field — ARR delta     |

The sync queries `Closed Won` opportunities and joins QuoteLineItems via `Quote.OpportunityId`.

---

## Local Development

### Prerequisites

- Go 1.21+
- Node 18+
- A Supabase project (free tier works)
- A Salesforce org with a Connected App

### 1. Clone and configure

```bash
cp .env.example .env
# Edit .env with your Salesforce credentials and DATABASE_URL
```

### 2. Start the Go backend

```bash
go mod tidy
go run main.go
```

Server starts on `http://localhost:8080`. On first run it:
- Auto-migrates the database schema
- Runs a full sync from Salesforce
- Starts a 24-hour background sync ticker

### 3. Start the React frontend

```bash
cd web
npm install
npm run dev
```

Frontend runs on `http://localhost:5173` and proxies `/api` to `:8080`.

---

## API Reference

| Method | Endpoint             | Description                                    |
|--------|----------------------|------------------------------------------------|
| GET    | `/api/summary`       | Aggregated ARR, MRR, Delta ARR, contract count |
| GET    | `/api/contracts`     | All contracts (`?stage=CLOSED_WON\|ALL`)       |
| POST   | `/api/sync`          | Incremental sync from Salesforce               |
| POST   | `/api/sync?full=true`| Full re-sync (ignores last sync timestamp)     |
| GET    | `/api/health`        | Liveness check                                 |

---

## Database Schema

```sql
contracts (
  id               SERIAL PRIMARY KEY,
  salesforce_id    TEXT UNIQUE NOT NULL,   -- Opportunity.Id
  account_name     TEXT,                   -- Opportunity.Account.Name
  deal_name        TEXT,                   -- Opportunity.Name
  stage_name       TEXT,                   -- e.g. "Closed Won"
  close_date       DATE,                   -- Opportunity.CloseDate
  arr              NUMERIC(18,2),          -- Opportunity.ARR__c
  delta_arr        NUMERIC(18,2),          -- SUM(QuoteLineItem.Ruby__DeltaARR__c)
  currency_code    TEXT,                   -- CurrencyIsoCode
  last_modified_at TIMESTAMPTZ,
  synced_at        TIMESTAMPTZ
)

sync_log (
  id          SERIAL PRIMARY KEY,
  synced_at   TIMESTAMPTZ DEFAULT NOW(),
  upserted    INTEGER,
  total       INTEGER,
  incremental BOOLEAN,
  error_msg   TEXT        -- NULL on success
)
```

---

## Production Deployment

### Option A — Railway (~$5/mo)

```bash
npm install -g @railway/cli
railway login
railway init
railway up
```

Set environment variables in the Railway dashboard (see `.env.example`).

### Option B — Render

1. Connect your GitHub repo
2. **New Web Service → Go**
3. Build command: `go build -o server .`
4. Start command: `./server`
5. Add environment variables

### Frontend on Vercel

```bash
cd web
npm run build
npx vercel --prod
```

Set `VITE_API_BASE=https://your-backend.railway.app` in Vercel env vars.

---

## Sync Behavior

- **Auto-sync**: runs every 24 hours via a background Go goroutine
- **Incremental**: fetches only Opportunities with `LastModifiedDate >` last successful sync
- **Manual sync**: click "Sync Now" in the UI, or `POST /api/sync`
- **Full re-sync**: click "Full Sync" in the UI, or `POST /api/sync?full=true`
- All sync runs are logged in the `sync_log` table
