# --- inforge user provisioning (appended) -----------------------------------
# Runs at first boot. Creates the deploy user when one is declared on the
# compute spec. The SSH key material comes from the environment-level
# deployPublicKey substituted at provision time.
set -euo pipefail

DEPLOY_USER='{{deploy_user}}'
DEPLOY_KEY='{{deploy_public_key}}'

if [ -n "$DEPLOY_USER" ] && [ -n "$DEPLOY_KEY" ]; then
  useradd --create-home --shell /bin/bash "$DEPLOY_USER" 2>/dev/null || true

  install -d -m 700 -o "$DEPLOY_USER" -g "$DEPLOY_USER" "/home/$DEPLOY_USER/.ssh"
  printf '%s\n' "$DEPLOY_KEY" > "/home/$DEPLOY_USER/.ssh/authorized_keys"
  chmod 600 "/home/$DEPLOY_USER/.ssh/authorized_keys"
  chown "$DEPLOY_USER:$DEPLOY_USER" "/home/$DEPLOY_USER/.ssh/authorized_keys"

  # Allow the deploy user to run deployment operations without a password prompt.
  printf '%s ALL=(ALL) NOPASSWD: ALL\n' "$DEPLOY_USER" > "/etc/sudoers.d/$DEPLOY_USER"
  chmod 440 "/etc/sudoers.d/$DEPLOY_USER"
fi
# --- end inforge user provisioning ------------------------------------------
