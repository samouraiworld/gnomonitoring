#!/bin/bash

# Choisir le endpoint
read -p "Choisir le type de webhook (gnovalidator / webhookgovdao) : " endpoint
if [[ "$endpoint" != "gnovalidator" && "$endpoint" != "webhookgovdao" ]]; then
  echo "❌ Endpoint invalide."
  exit 1
fi

# Saisie des données
read -p "Nom de l'utilisateur : " user
read -p "URL du webhook : " url
read -p "Type de webhook (discord / slack) : " type

# Construction et envoi de la requête
json_payload=$(jq -n \
  --arg user "$user" \
  --arg url "$url" \
  --arg type "$type" \
  '{user: $user, url: $url, type: $type}'
)

echo "📡 Envoi vers http://localhost:8989/$endpoint..."
curl -X POST "http://localhost:8989/$endpoint" \
  -H "Content-Type: application/json" \
  -d "$json_payload"
