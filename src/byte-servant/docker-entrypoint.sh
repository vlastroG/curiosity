#!/bin/sh
set -e

cat > /usr/share/nginx/html/env-config.js <<EOF
window._env_ = {
  DEEPSEEK_API_KEY: "${DEEPSEEK_API_KEY}"
};
EOF

exec nginx -g 'daemon off;'
