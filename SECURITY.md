# Security policy

## Reporting a vulnerability

Report vulnerabilities through GitHub's private advisory flow:

https://github.com/v1b3coder/keryx/security/advisories/new

Do not open a public issue for a vulnerability. Public issues are visible to
everyone and are read by the automated triage agent, so a report that reaches the
issue tracker is already disclosed.

Please include:

- the affected component: `sdk`, `relay`, `app`, `demo` or `spec`,
- the version, commit or app build,
- what an attacker can do and what they need to start,
- the smallest reproduction you have.

## Scope

In scope: the protocol specification in `spec/`, the publisher SDK and the `pub`
CLI in `sdk/`, the notification relay in `relay/`, the PWA / React Native client
in `app/`, and the demo generator in `demo-tool/`.

Out of scope: vulnerabilities that are already public upstream, and the demo
deployment treated as a hosting service — it is a test artifact, not production
infrastructure.

## What not to send

Never include private keys, tokens or credentials from your own deployment,
including the demo keystore. Describe what a secret is used for instead of pasting
it.

## After the report

The maintainer confirms the report in the advisory, prepares a fix, and coordinates
disclosure there. Protocol and relay fixes ship together with the clients that
consume them, so a fix may take longer than a plain app change.
