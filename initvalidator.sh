#!/bin/sh
set -e  # Arrête le script en cas d'erreur

echo "🔍 Vérification si db exits."
if [ ! -f /gnoroot/db/state.db ]; then
    echo "📜 genesis.json non trouvé, génération..."
    gnoland  secrets init 
   

    
fi

echo "🚀 Démarrage de Gnoland..."
exec gnoland start config /gnoroot/gnoland-data/config/config.toml