# Confidential-computing attestation

ARTEX Defense exposes fresh dstack evidence so the operator can verify the runtime
before giving it sensitive source code, findings, credentials, or model access. The
verification UI is under **System → Attestation**.

## What is verified

The browser generates a random 32-byte nonce and requests:

- `GET /attestation?reportData=<hex nonce>` for the hardware quote, event log, and
  Google vTPM attestation when the guest reports `cloud_vendor=Google`;
- `GET /info` for the application identity, Compose hash, OS image hash, TCB
  measurements, and measured application Compose document.

The browser then checks that:

1. the verified report data begins with the fresh nonce;
2. an external verifier accepts the TDX quote or the combined Google attestation;
3. the SHA-256 digest of the measured application Compose document matches its
   `compose-hash` event; and
4. the image references in that Compose document match an approved, signed ARTEX
   Defense release.

The first three checks are performed by the page. The fourth is an operator policy
decision: compare the displayed digest-pinned image with the digest and Cosign
identity in the corresponding GitHub release.

## Deployment requirements

The ARTEX Defense container needs access to the dstack guest-agent Unix socket:

```yaml
services:
  artex-defense:
    image: ${ARTEX_DEFENSE_IMAGE:?set ARTEX_DEFENSE_IMAGE to a digest-pinned ARTEX Defense release image}
    volumes:
      - /var/run/dstack.sock:/var/run/dstack.sock
```

Set `ARTEX_DEFENSE_IMAGE` to the immutable `repository@sha256:...` value from the
GitHub release. Each maintained release also attaches
`docker-compose.attested.yml`, with that exact digest already inserted, and a matching
`artex-defense-release.json` manifest and `SHA256SUMS` file. Use the release asset as
the measured application Compose input rather than a Compose file containing a mutable
tag.

Set `DSTACK_SOCKET` only when the guest agent uses a different socket path. Release
images compile `NEXT_PUBLIC_DSTACK_VERIFIER_URL` as
`https://verify.adverserial.ai/verify`; custom source builds can supply a different
external verifier through the same Docker build argument.

The attestation endpoints deliberately return `Cache-Control: no-store`. A failure to
read dstack runtime metadata, obtain a quote, or obtain the additional Google
attestation causes the request to fail rather than returning partial evidence.

## Independent verification

Download the evidence JSON from the Attestation page and verify it on a separate
trusted machine. The verifier must run outside the confidential VM being evaluated.
Recheck the fresh nonce, hardware evidence, RTMR measurements, Compose hash, and every
image digest against the approved release policy. A passing quote proves the measured
runtime identity and nonce binding; it does not prove that the application source,
dependencies, external model provider, KMS, or governance policy is free of defects.

Local Docker environments normally have no dstack socket, so the endpoints return an
unavailable-evidence error there. This is expected and must not be presented as a
successful attestation.
