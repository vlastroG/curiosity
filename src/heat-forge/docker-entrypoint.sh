#!/bin/sh
set -e

cat > /usr/share/nginx/html/env-config.js <<CONF
window._env_ = {
  DEEPSEEK_API_KEY: "${DEEPSEEK_API_KEY}"
};
CONF

exec nginx -g 'daemon off;'
