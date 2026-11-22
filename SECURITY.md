# Security policy

## Supported versions

Only the latest release gets security fixes. `flockadoo update` moves a provider to it.

## Reporting a vulnerability

Report privately: open the repository's **Security** tab and choose **Report a vulnerability**, or go straight to <https://github.com/Flockadoo/flockadoo/security/advisories/new>.

Please do not open a public issue, pull request or discussion about a vulnerability.

Include:

- the version (`flockadoo --version`) and operating system
- what an attacker can do, and what they need first (network position, local access, a provider or consumer account)
- the steps to reproduce it, or a proof of concept

We acknowledge reports within a few working days and keep you updated until the issue is fixed or closed. With your agreement, we credit you in the advisory.

## Scope

This repository holds the provider client. In scope:

- the relay between the Flockadoo gateway and the local engine
- the self-updater and the install scripts
- engine installation and start-up (vLLM, vLLM Metal, llama.cpp)
- the flock and Winglink code
- how the API key, node token and Winglink key are stored, sent and shown

Problems in the Flockadoo platform itself (the website, the API, the gateway) are welcome through the same form.

## Known limitation: model verification

The client hashes a model to identify it. That hash does not prove which model is serving: safetensors are hashed by header only, and a modified client can report anything. The platform must not treat the client's verification as proof. A report that a modified client can lie about its model, hardware or capacity describes this known limitation, not a new vulnerability.
