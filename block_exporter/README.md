# Gno Block Exporter

Simple Prometheus metrics exporter for monitoring a Gno blockchain validator.

## What it does

Monitors your validator in real-time and exports Prometheus metrics:
- `gnoland_missed_blocks` - Cumulative count of missed blocks (never resets)
- `gnoland_consecutive_missed_blocks` - Current consecutive missed blocks (resets to 0 when validator signs)

## Quick Start

1. **Configure**
```bash
cp config.yaml.template config.yaml
```

Edit `config.yaml`:
```yaml
rpc_endpoint: "https://rpc.test9.testnets.gno.land"
validator_address: "your_validator_address_here"
metrics_port: 8888
```

2. **Run with Docker**
```bash
docker compose up -d
```

3. **View metrics**
Open http://localhost:8888/metrics

That's it! Your validator monitoring is ready.