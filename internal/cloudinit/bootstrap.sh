
# --- inforge bootstrap (appended) -------------------------------------------
# Runs once at first boot. If bootstrap.yaml is present, the manifest contains
# secrets encrypted with SOPS/age to key K. This step redeems the one-time
# token with the escrow service for K, decrypts the manifest secret fields,
# then re-encrypts them to the host's own SSH key so K is never needed again.
set -euo pipefail

BOOTSTRAP_FILE=/etc/wardnet/bootstrap.yaml
MANIFEST_FILE=/etc/wardnet/manifest.yaml
AGE_KEY_FILE=/root/.config/sops/age/keys.txt

# Write the bootstrap doc provisioned at VM creation time, if any.
bootstrap_content='{{bootstrap_doc}}'
if [ -n "$bootstrap_content" ]; then
  mkdir -p "$(dirname "$BOOTSTRAP_FILE")"
  printf '%s\n' "$bootstrap_content" > "$BOOTSTRAP_FILE"
  chmod 600 "$BOOTSTRAP_FILE"
fi

# No bootstrap.yaml => the manifest has no secrets; nothing to do.
[ -f "$BOOTSTRAP_FILE" ] || exit 0

escrow_url=$(yq -r '.escrow_url' "$BOOTSTRAP_FILE")
token=$(yq -r '.token' "$BOOTSTRAP_FILE")
tenant=$(yq -r '.tenant' "$BOOTSTRAP_FILE")

# Redeem the one-time token for the age identity K (one redemption only).
# The repository field scopes the lookup so tokens can't cross repo boundaries.
mkdir -p "$(dirname "$AGE_KEY_FILE")"
curl -fsSL -X POST "$escrow_url/bootstrap" \
  -H "Content-Type: application/json" \
  --data "{\"token\":\"$token\",\"repository\":\"$tenant\"}" | jq -r '.key' > "$AGE_KEY_FILE"
chmod 600 "$AGE_KEY_FILE"

# Decrypt in place with K, then re-key the SOPS data key to the host SSH key
# (age accepts ssh-ed25519 recipients) so future boots decrypt without K.
host_recipient=$(cat /etc/ssh/ssh_host_ed25519_key.pub)
export SOPS_AGE_KEY_FILE="$AGE_KEY_FILE"
sops updatekeys --yes --age "$host_recipient" "$MANIFEST_FILE"

# K is no longer required on this host.
shred -u "$AGE_KEY_FILE" 2>/dev/null || rm -f "$AGE_KEY_FILE"
rm -f "$BOOTSTRAP_FILE"
# --- end inforge bootstrap --------------------------------------------------
