# add webhook

```bash
url -X POST http://localhost:8080/webhook \
  -H "Content-Type: application/json" \
  -d '{"url": "URL_WEBHOOK", "type": ["discord"/"slacl"}'
```

# List of webhook

```bash
curl http://localhost:8080/webhooks

```

# Delete webhook

```bash
 curl -X DELETE "http://localhost:8080/webhooks?id=2"


```
