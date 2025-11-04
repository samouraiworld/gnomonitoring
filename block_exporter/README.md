
## 🚀 Block Exporter

![Dashboard principal](assets/Block_Exporter.png)

### Requirements

- [Docker](https://www.docker.com/)
- [Docker Compose](https://docs.docker.com/compose/)

### Setup

1. Copy the configuration template and edit it:

```bash
cd block_exporter
cp config.yaml.template config.yaml 
nano config.yaml
```

2. Configure your validator address and (optionally) the RPC endpoint:

```yaml
rpc_endpoint: "https://rpc.test8.testnets.gno.land"
validator_address: "replace with your validator address"
port: 8888
```

3. Start the container:

```bash
touch webhooks.db
docker compose up -d 
```

4. Open <http://localhost:8888/metrics> to view the following metrics:

- `gnoland_missed_blocks`
- `gnoland_consecutive_missed_blocks`

---
