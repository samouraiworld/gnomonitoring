# Gno Monitoring Backend

Comprehensive monitoring service for Gno blockchain validators with Discord/Slack alerts, governance monitoring, and Prometheus metrics.

## Quick Start (In local)

1. **Configure**
```bash
cp config.yaml.template config.yaml
nano config.yaml
```

Set `dev_mode: true` in `config.yaml` to bypass Clerk authentication.

2. **Run with Docker**
```bash
docker compose up -d
```

3. **Access Services**
- **API**: http://localhost:8989
- **Metrics**: http://localhost:8888/metrics

4. **Test API in local without authentication (GovDAO):**
```bash
# Create webhook (uses default "local-dev-user")
curl -X POST http://localhost:8989/webhooks/govdao \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://discord.com/api/webhooks/YOUR_WEBHOOK",
    "type": "discord",
    "description": "Test webhook"
  }'

# Or specify custom user ID
curl -X POST http://localhost:8989/webhooks/govdao \
  -H "X-Debug-UserID: alice" \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://discord.com/api/webhooks/YOUR_WEBHOOK",
    "type": "discord",
    "description": "Alice\'s webhook"
  }'

# List the available webhooks
curl http://localhost:8989/webhooks/govdao

# Delete a webhook
curl -X DELETE "http://localhost:8989/webhooks/govdao?id=1"
```

Also available for validators with the endpoint `/webhooks/validator`.

## API Reference

### 🔐 Authentication

**Production Mode**: Most endpoints require Clerk authentication. Include your Clerk session token in the `Authorization` header:
```bash
curl -H "Authorization: Bearer YOUR_SESSION_TOKEN" \
     http://localhost:8989/endpoint
```

**Development Mode**: When `dev_mode: true` is set in config, authentication is bypassed:
```bash
# Use default dev user
curl http://localhost:8989/webhooks/govdao

# Or specify custom user ID
curl -H "X-Debug-UserID: my-test-user" \
     http://localhost:8989/webhooks/govdao
```

### 📊 Public Dashboard Endpoints

These endpoints don't require authentication:

#### Get Block Height
```bash
GET /block_height
```
```bash
curl http://localhost:8989/block_height
```

#### Get Latest Incidents
```bash
GET /latest_incidents
```
```bash
curl http://localhost:8989/latest_incidents
```

#### Get Validator Participation
```bash
GET /Participation?period=[current_week|current_month|current_year]
```
```bash
curl "http://localhost:8989/Participation?period=current_week"
```

### 🎣 Webhook Management

#### GovDAO Webhooks (Governance Alerts)

**List GovDAO Webhooks**
```bash
GET /webhooks/govdao
```
```bash
curl -H "Authorization: Bearer TOKEN" \
     http://localhost:8989/webhooks/govdao
```

**Create GovDAO Webhook**
```bash
POST /webhooks/govdao
```
```bash
curl -X POST http://localhost:8989/webhooks/govdao \
  -H "Authorization: Bearer TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://discord.com/api/webhooks/YOUR_WEBHOOK",
    "type": "discord",
    "description": "Governance alerts"
  }'
```

**Update GovDAO Webhook**
```bash
PUT /webhooks/govdao
```
```bash
curl -X PUT http://localhost:8989/webhooks/govdao \
  -H "Authorization: Bearer TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "id": 1,
    "url": "https://hooks.slack.com/YOUR_WEBHOOK",
    "type": "slack",
    "description": "Updated governance alerts"
  }'
```

**Delete GovDAO Webhook**
```bash
DELETE /webhooks/govdao?id=ID
```
```bash
curl -X DELETE "http://localhost:8989/webhooks/govdao?id=1" \
     -H "Authorization: Bearer TOKEN"
```

#### Validator Webhooks (Validator Alerts)

**List Validator Webhooks**
```bash
GET /webhooks/validator
```
```bash
curl -H "Authorization: Bearer TOKEN" \
     http://localhost:8989/webhooks/validator
```

**Create Validator Webhook**
```bash
POST /webhooks/validator
```
```bash
curl -X POST http://localhost:8989/webhooks/validator \
  -H "Authorization: Bearer TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://discord.com/api/webhooks/YOUR_WEBHOOK",
    "type": "discord",
    "description": "Validator monitoring alerts"
  }'
```

**Update Validator Webhook**
```bash
PUT /webhooks/validator
```

**Delete Validator Webhook**
```bash
DELETE /webhooks/validator?id=ID
```
```bash
curl -X DELETE "http://localhost:8989/webhooks/validator?id=1" \
     -H "Authorization: Bearer TOKEN"
```

### 👥 User Management

**Create User**
```bash
POST /users
```
```bash
curl -X POST http://localhost:8989/users \
  -H "Authorization: Bearer TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "email": "user@example.com",
    "name": "John Doe"
  }'
```

**Get User**
```bash
GET /users
```
```bash
curl -H "Authorization: Bearer TOKEN" \
     http://localhost:8989/users
```

**Update User**
```bash
PUT /users
```

**Delete User**
```bash
DELETE /users
```

### 📧 Alert Contacts

**Create Alert Contact**
```bash
POST /alert-contacts
```
```bash
curl -X POST http://localhost:8989/alert-contacts \
  -H "Authorization: Bearer TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "email": "alerts@example.com",
    "name": "Alert Manager",
    "active": true
  }'
```

**List Alert Contacts**
```bash
GET /alert-contacts
```

**Update Alert Contact**
```bash
PUT /alert-contacts
```

**Delete Alert Contact**
```bash
DELETE /alert-contacts?id=ID
```

### ⏰ Report Schedule

**Get Report Schedule**
```bash
GET /usersH
```

**Update Report Schedule**
```bash
PUT /usersH
```
```bash
curl -X PUT http://localhost:8989/usersH \
  -H "Authorization: Bearer TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "hour": 10,
    "minute": 30
  }'
```

## Configuration

Edit `config.yaml`:

```yaml
interval_seconde: 1200                    # Monitoring interval
backend_port: "8989"                      # API port
allow_origin: "http://localhost:3000"    # CORS origin
rpc_endpoint: "https://rpc.test9.testnets.gno.land"
windows_size: 100                         # Block window for calculations
daily_report_hour: 10                     # Daily report hour (24h format)
daily_report_minute: 30                   # Daily report minute
metrics_port: 8888                        # Prometheus metrics port
gnoweb: "https://test9.testnets.gno.land"
graphql: "indexer.test9.testnets.gno.land/graphql/query"
clerk_secret_key: "sk_test_..."          # Clerk authentication key
dev_mode: false                           # Set to true for local development
```

### Development vs Production Mode

**For Local Development:**
```yaml
dev_mode: true                            # Enable development mode
clerk_secret_key: ""                     # Can be empty in dev mode
```

**For Production:**
```yaml
dev_mode: false                           # Disable development mode (default)
clerk_secret_key: "sk_live_your_key"     # Required in production
```

## Prometheus Metrics

Available at `http://localhost:8888/metrics`:

- `gnoland_validator_participation_rate{validator_address, moniker}` - Validator participation percentage
- `gnoland_missed_blocks{validator_address, moniker}` - Total missed blocks today
- `gnoland_consecutive_missed_blocks{validator_address, moniker}` - Current consecutive missed blocks

## Alert Types

- **CRITICAL**: 30+ missed blocks
- **WARNING**: 5+ missed blocks  
- **RESOLVED**: Validator back online
- **INFO**: General notifications (new validators, network issues)
