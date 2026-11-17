# Releasing ARTEX Attack

A release is a version tag on `master`. Pushing the tag is the whole procedure:

```bash
git checkout master && git pull
git tag v0.1.6 && git push origin v0.1.6
```

The `release` workflow then builds the image from that exact commit, pushes it to the registry
hosted agents pull from, signs it, and creates a GitHub release that lists the image digest and
the command to verify the signature. It refuses a tag that is not on `master`.

## What makes a release trustworthy

- **Pinned by digest.** Agents never pull a tag or "latest". Their measured configuration names
  the digest, so the software in a customer's confidential machine changes only when an agent is
  deliberately launched with a new release.
- **No stored keys or secrets.** The workflow signs with a short-lived certificate tied to this
  repository, this workflow file and the tag, and signs in to Google Cloud the same way. There is
  nothing in the repository settings to leak.
- **Public record.** Each signature is written to the public Sigstore transparency log. That
  reveals the repository name, the workflow path and the tag, not the source code.
- **Checked before use.** The agents' deployment repository keeps a list of approved releases and
  refuses to launch an agent with an image that is not on it.

## After a release

Add the release to `config/approved-releases.json` in `AdverserialAI/agent-deployment` after
verifying its signature with the command from the release notes. Existing agents keep the release
they were launched with until they are relaunched.

## One-time setup

The link that lets this workflow push to the registry without a secret is created by
`scripts/configure-release-publisher.sh` in `AdverserialAI/agent-deployment`.
