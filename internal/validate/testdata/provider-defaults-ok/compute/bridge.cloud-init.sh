#!/bin/bash
set -euo pipefail

# Minimal cloud-init template for validation testdata. Placeholders are
# substituted by internal/cloudinit at provision time.
echo "domain={{domain}}"
echo "instance={{instance}}"
cat <<'KEYEOF' > /home/deploy/.ssh/authorized_keys
{{deploy_public_key}}
KEYEOF
cat <<'MANIFESTEOF' > /etc/wardnet/manifest.yaml
{{manifest}}
MANIFESTEOF
