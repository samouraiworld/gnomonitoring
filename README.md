# 🛠️ Monitoring Gnoland Validators

This repository provides lightweight tools to monitor the [Gno.land](https://gno.land) blockchain and its validators.

🧩 Architecture Overview:

![Architecture Overview](assets/gnomonitoring_infra.png)

Two services are available:

- **GovDAO** – Detect news proposal and  status (ACCEPTED / REFUSED / IN PROGRESS).

- **Validator Alerting** – Monitors the entire validator set, calculates participation rates  and sends Discord/Slack/Telegram alerts when needed. Also exposes Prometheus metrics.

---

### ✅ Gno Validator Monitoring

**Sends alerts (Discord/Slack) when:**

- Rpc is down
- The blockchain is stuck on the same block for more than 2 minutes.
- A validator's participation rate drops below 100%.
- A new validator joins the network.

**Expose Metrics from API REST** :

### 🧾 GovDAO Proposal Detection

Sends Discord alerts when a new proposal is detected on:
<https://test9.testnets.gno.land/r/gov/dao>

### 🛠️ Setup

**Requirements**

- [Docker](https://www.docker.com/)
- [Docker Compose](https://docs.docker.com/compose/)

![Discord alert daily](assets/discord_view.png)

1. Copy the configuration template and edit it:

``` bash
cd backend 
cp config.yaml.template config.yaml 
nano config.yaml
```

2. Customize parameters as needed. For example:

```yaml

backend_port: "8989"
allow_origin: "http://localhost:3000"
rpc_endpoint: "https://rpc.test9.testnets.gno.land"
metrics_port: 8888
gnoweb: "https://test9.testnets.gno.land"
graphql: "indexer.test9.testnets.gno.land/graphql/query"
clerk_secret_key: "sk_test...." #change me
dev_mode: false # Set to true for local development without Clerk auth
token_telegram_validator: ""
token_telegram_govdao: ""
```

3. Start the backend:

```bash
docker compose up -d 
```

---

### 🔗 Webhook Management (Discord / Slack)

Manage webhooks for alert delivery. Choose between validator or govdao depending on the type.

**➕ Add a webhook**

```bash
curl -X POST http://localhost:8989/webhooks/[validator / govdao]\
  -H "Content-Type: application/json" \
  -d '{"user": "username","url": "URL_WEBHOOK", "type": ["discord"/"slack"}'
```

**📋 List webhooks**

```bash
curl http://localhost:8989/webhooks/[validator / govdao]
```

**❌ Delete a webhook**

```bash
 curl -X DELETE "http://localhost:8989/webhooks/[validator / govdao]?id=x"
```

---

### 📈 Prometheus Metrics

Metrics are exposed at <http://localhost:8888/metrics>.

![Status of Validator](assets/status_of_validator.png)

Example metrics:

- `gnoland_validator_participation_rate{moniker="samourai-dev-team-1",validator_address="g1tq3gyzjmuu4gzu4np4ckfgun87j540gvx43d65"} 100`
- `gnoland_block_window_start_height 100`
- `gnoland_block_window_end_height 199`
