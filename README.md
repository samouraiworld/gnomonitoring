# Monitoring GnoLand Validators

Here we provide tools to monitor the Gnoland blockchain and its validators.
Two services are available:

- **block_exporter:** Allows you to track missed blocks from your Gnoland validator. It exposes metrics for Prometheus, enabling the creation of dashboards and alerts using Grafana.

- **GnolandStatus**: Allows you to monitor the state of the Gnoland blockchain across all validators. It calculates a participation rate over a defined block window and sends a Discord alert when a validator's participation rate drops below 100%. It also exposes metrics for Prometheus.

## BLOCK EXPORTER

![Dashboard principal](assets/Block_Exporter.png)
You need to have Docker and Docker Compose installed on the server.

First, you need to configure the YAML configuration file :

```
cd block_exporter
cp config.yaml.template config.yaml 
nano config.yaml
```

You must specify your validator's address, and it's also possible to change the RPC service address.

```
rpc_endpoint: "https://rpc.test6.testnets.gno.land"
validator_address: "replace with your validator address"
port: 8888
```

Now you just need to start the Docker container:

```
docker compose up -d 
```

And now, at the URL <http://localhost:8888/metrics>, you can view the following metrics:

- gnoland_missed_blocks
- gnoland_consecutive_missed_blocks

## Gnoland Status

![Discord alert dayli ](assets/discord_view.png)
You need to have Docker and Docker Compose installed on the server.
First, you need to configure the YAML configuration file :

```
cd Gnolandstatus 
cp config.yaml.template config.yaml 
nano config.yaml
```

You must provide the Discord webhook URL. You can adjust the other parameters as you wish.
Window size is the size of the sliding window used to calculate the participation rate.

```
rpc_endpoint: "https://rpc.test6.testnets.gno.land"
discord_webhook_url : ""
windows_size : 100 
daily_report_hour: 16 
daily_report_minute: 0
metrics_port: 8888
```

Now you just need to start the Docker container:

```
docker compose up -d 
```

And now, at the URL <http://localhost:8888/metrics>, you can view the following metrics:
![Status of Validator](assets/status_of_validator.png)

- gnoland_validator_participation_rate{moniker="samourai-dev-team-1",validator_address="g1tq3gyzjmuu4gzu4np4ckfgun87j540gvx43d65"} 100
- gnoland_block_window_start_height 100
- gnoland_block_window_end_height 199
