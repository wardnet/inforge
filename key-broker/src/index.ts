import { verifyGitHubOIDC } from "./oidc";

export interface Env {
  BOOTSTRAP_KV: KVNamespace;
}

interface TokenBody {
  token: string;
  key:   string;
}

interface BootstrapBody {
  token:      string;
  repository: string;
}

// kvKey builds the tenant-scoped KV key so that a token minted by repo A
// cannot be used to redeem a key registered by repo B.
function kvKey(repository: string, token: string): string {
  return `${repository}:${token}`;
}

async function handlePutToken(request: Request, env: Env): Promise<Response> {
  const authHeader = request.headers.get("Authorization");
  if (!authHeader?.startsWith("Bearer ")) {
    return new Response("Unauthorized", { status: 401 });
  }

  const jwt = authHeader.slice("Bearer ".length).trim();
  const repository = await verifyGitHubOIDC(jwt);
  if (repository === null) {
    return new Response("Unauthorized", { status: 401 });
  }

  let body: TokenBody;
  try {
    body = (await request.json()) as TokenBody;
  } catch {
    return new Response("Bad Request: invalid JSON", { status: 400 });
  }

  const { token, key } = body;
  if (typeof token !== "string" || token.length === 0 ||
      typeof key   !== "string" || key.length   === 0) {
    return new Response("Bad Request: missing fields", { status: 400 });
  }

  await env.BOOTSTRAP_KV.put(kvKey(repository, token), key, { expirationTtl: 1800 });
  return new Response("OK", { status: 200 });
}

async function handlePostBootstrap(request: Request, env: Env): Promise<Response> {
  let body: BootstrapBody;
  try {
    body = (await request.json()) as BootstrapBody;
  } catch {
    return new Response("Bad Request: invalid JSON", { status: 400 });
  }

  const { token, repository } = body;
  if (typeof token !== "string" || token.length === 0) {
    return new Response("Bad Request: missing token", { status: 400 });
  }
  if (typeof repository !== "string" || repository.length === 0) {
    return new Response("Bad Request: missing repository", { status: 400 });
  }

  const key = await env.BOOTSTRAP_KV.get(kvKey(repository, token));
  if (key === null) {
    return new Response("Not Found", { status: 404 });
  }

  // Burn the token — one use only
  await env.BOOTSTRAP_KV.delete(kvKey(repository, token));

  return Response.json({ key }, { status: 200 });
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);

    if (request.method === "PUT" && url.pathname === "/token") {
      return handlePutToken(request, env);
    }

    if (request.method === "POST" && url.pathname === "/bootstrap") {
      return handlePostBootstrap(request, env);
    }

    return new Response("Not Found", { status: 404 });
  },
} satisfies ExportedHandler<Env>;
