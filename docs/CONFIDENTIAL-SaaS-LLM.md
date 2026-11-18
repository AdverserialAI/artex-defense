# Confidential SaaS LLM bootstrap

The Adverserial Agents deployment uses `ARTEX_LLM_BOOTSTRAP_FILE` to configure
ARTEX after the confidential runtime has been admitted. This mode has higher
precedence than database profiles and ordinary environment variables.

The bootstrap document contains provider metadata and a path to a separate
credential file. It never contains the credential itself:

```json
{
  "version": 1,
  "providers": [
    {
      "provider": "openai",
      "model": "adverserial/cyberseek",
      "transport": "attested-gateway",
      "base_url": "http://attested-inference-gateway:8787/v1",
      "api_key_file": "/run/adverserial/current/attested-gateway-token"
    }
  ]
}
```

The HTTP URL above is a private Docker-network hop inside the same measured
confidential VM. It is not a public or ordinary CyberSEEK API endpoint. The
credential is a random per-agent capability accepted only by the sibling
attested-inference gateway; it is never the user's platform API key.

For managed CyberSEEK, ARTEX accepts exactly this model, transport, internal
gateway URL and local-token path. It rejects direct access to
`api.adverserial.ai`, caller-selected endpoints and every ordinary API
fallback. The sibling gateway applies the ACK confidential-inference protocol:
it verifies fresh Intel TDX and NVIDIA evidence and the pinned production
policy, pins the quote-bound TLS identity, obtains a single-request wallet
entitlement, uses encrypted request/response transport and releases a response
only after its signed inference receipt verifies.

ARTEX accepts only `anthropic`, `openai`, or `openai-responses`, requires HTTPS
for a custom endpoint, restricts both files to the guard-owned
`/run/adverserial/current` tree, rejects broad file permissions, and bounds file
sizes. If this bootstrap is configured but invalid, ARTEX stays idle. It does
not fall back to an environment key.

While measured-runtime bootstrap mode is active:

- the bootstrap overrides persisted LLM profiles;
- task and conversation profile pins cannot override it;
- the legacy LLM update endpoint and profile-save endpoint return HTTP 423;
- the key is held in memory only for provider requests and is not copied into
  PostgreSQL;
- the key file is mounted read-only from the runtime guard's atomic secret
  generation.

For Anthropic or OpenAI BYOK, the key is decrypted only in the confidential VM.
It must still be sent to the selected external provider over certificate-verified
TLS to authenticate requests. Do not describe an external-provider key as
literally never leaving the TEE. The accurate guarantee is that it is never
stored in the public portal, its database, Docker environment metadata, ARTEX
PostgreSQL profiles, or application logs.

## Connecting a model after the deployment exists

A hosted deployment usually starts before its owner has given it a model. Three
additions cover that case:

- **Waiting is a valid state.** A bootstrap with an empty provider list is
  accepted. ARTEX stays idle, the ordinary profile endpoints stay locked, and no
  environment key is used as a fallback.
- **Changes are picked up without a restart.** ARTEX re-reads the bootstrap when
  the document or a key file it names changes (checked every five seconds). The
  document is written by the deployment's front door inside the same
  confidential VM, on a page only the owner can reach; ARTEX has it read-only.
  Removing the provider takes the engine back to idle.
- **Compatible endpoints.** A provider marked `"endpoint": "custom"` may use any
  public HTTPS `base_url`, for a self-hosted or third-party OpenAI- or
  Anthropic-compatible service. Plain HTTP, addresses with credentials or a
  query, `localhost`, private, link-local and metadata addresses, and bare
  container names are refused. Without the mark, only the provider's own host is
  accepted, as before.

```json
{
  "version": 1,
  "providers": [
    {
      "provider": "openai",
      "model": "my-model",
      "endpoint": "custom",
      "base_url": "https://llm.example.com/v1",
      "api_key_file": "/run/adverserial/current/llm/key-openai"
    }
  ]
}
```

A deployment whose ARTEX container has its own route to the Internet and no
sibling egress proxy sets `ARTEX_LLM_BOOTSTRAP_EGRESS=direct` in its measured
configuration. Otherwise provider traffic still goes through the proxy.

The key file is readable by the ARTEX process, so a tool ARTEX runs can read it
too. Keeping the key in a separate container that adds it to outgoing requests
would close that; it is not done here.
