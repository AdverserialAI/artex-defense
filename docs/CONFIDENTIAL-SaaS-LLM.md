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
