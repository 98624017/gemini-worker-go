/**
 * Derive ownerHash from API key using HMAC-SHA256.
 * Must match the Go backend's security.DeriveOwnerHash() exactly.
 */
export async function deriveOwnerHash(
  secret: string,
  authorizationHeader: string
): Promise<string> {
  const token = normalizeBearerToken(authorizationHeader);

  const encoder = new TextEncoder();
  const key = await crypto.subtle.importKey(
    "raw",
    encoder.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"]
  );

  const signature = await crypto.subtle.sign(
    "HMAC",
    key,
    encoder.encode(token)
  );

  return arrayBufferToHex(signature);
}

function normalizeBearerToken(header: string): string {
  const trimmed = header.trim();
  if (!trimmed) {
    throw new Error("authorization header is required");
  }

  const parts = trimmed.split(/\s+/);
  if (parts.length !== 2 || parts[0].toLowerCase() !== "bearer") {
    throw new Error("authorization header must use Bearer token");
  }
  if (!parts[1]) {
    throw new Error("authorization token is required");
  }

  return parts[1];
}

function arrayBufferToHex(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer);
  let hex = "";
  for (let i = 0; i < bytes.length; i++) {
    hex += bytes[i].toString(16).padStart(2, "0");
  }
  return hex;
}
