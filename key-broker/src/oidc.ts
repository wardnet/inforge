interface JwtHeader {
  alg: string;
  kid: string;
}

interface JwkKey {
  kid: string;
  kty: string;
  alg: string;
  use: string;
  n: string;
  e: string;
}

interface Jwks {
  keys: JwkKey[];
}

/** Decode a base64url segment to a Uint8Array without using atob directly on url-safe chars. */
function base64urlDecode(input: string): Uint8Array {
  const base64 = input.replace(/-/g, "+").replace(/_/g, "/");
  const padded = base64 + "==".slice(0, (4 - (base64.length % 4)) % 4);
  const binary = atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

/** Decode a base64url segment to a plain string (UTF-8 assumed ASCII-safe for JSON). */
function base64urlDecodeToString(input: string): string {
  const bytes = base64urlDecode(input);
  return new TextDecoder().decode(bytes);
}

/**
 * Verify a GitHub Actions OIDC JWT and return the verified `repository` claim.
 *
 * Returns the `repository` string (e.g. "owner/repo") on success, or null if
 * the JWT is invalid, the signature fails, or the issuer doesn't match.
 * The repository claim is used by callers to namespace KV keys so that tokens
 * minted by one repo cannot redeem keys registered by another.
 */
export async function verifyGitHubOIDC(jwt: string): Promise<string | null> {
  const parts = jwt.split(".");
  if (parts.length !== 3) return null;

  const [headerB64, payloadB64, signatureB64] = parts;

  let header: JwtHeader;
  try {
    header = JSON.parse(base64urlDecodeToString(headerB64)) as JwtHeader;
  } catch {
    return null;
  }

  if (header.alg !== "RS256") return null;

  const { kid } = header;

  let jwks: Jwks;
  try {
    const response = await fetch(
      "https://token.actions.githubusercontent.com/.well-known/jwks",
      { cf: { cacheTtl: 3600 } } as RequestInit,
    );
    if (!response.ok) return null;
    jwks = (await response.json()) as Jwks;
  } catch {
    return null;
  }

  const jwk = jwks.keys.find((k) => k.kid === kid);
  if (!jwk) return null;

  let cryptoKey: CryptoKey;
  try {
    cryptoKey = await crypto.subtle.importKey(
      "jwk",
      jwk,
      { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" },
      false,
      ["verify"],
    );
  } catch {
    return null;
  }

  const signingInput = new TextEncoder().encode(`${headerB64}.${payloadB64}`);
  const signature = base64urlDecode(signatureB64);

  let valid: boolean;
  try {
    valid = await crypto.subtle.verify(
      "RSASSA-PKCS1-v1_5",
      cryptoKey,
      signature,
      signingInput,
    );
  } catch {
    return null;
  }

  if (!valid) return null;

  let claims: { iss?: string; repository?: string };
  try {
    claims = JSON.parse(base64urlDecodeToString(payloadB64)) as {
      iss?: string;
      repository?: string;
    };
  } catch {
    return null;
  }

  if (claims.iss !== "https://token.actions.githubusercontent.com") return null;
  if (!claims.repository) return null;
  return claims.repository;
}
