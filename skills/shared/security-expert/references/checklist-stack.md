# Checklist for the team's stack

To open at step 3, only for the categories the diff touches. Each item is a question to verify on the code, not a conclusion: a check that seems to be missing here may sit upstream (step 1).

## Next.js and React

- Versions of `next` and `react` with the fixes for the known critical CVEs (CVE-2025-55182 on React Server Components: Next.js bundles them in its own package, so `next` is what gets updated, and its CVE-2025-66478 was rejected as a duplicate; CVE-2025-29927, middleware bypass): verified on the **lockfile**, never from memory.
- Authorization **inside** every Server Action and every route handler, not only in the middleware: the middleware as the only check is a finding to verify.
- Server Components that pass to the client more data than needed (whole database objects, internal fields).
- Secrets in `NEXT_PUBLIC_*` variables or ended up in the client bundle.
- `dangerouslySetInnerHTML` with content that comes from the user or from an LLM.
- Security headers: CSP, HSTS.

## WordPress and PHP

- Nonce and `current_user_can` on every action that modifies.
- `$wpdb->prepare` on every query with input; `$wpdb->query` with concatenation is the typical candidate.
- Output escaping: `esc_html`, `esc_attr`, `esc_url`.
- Uploads with `wp_handle_upload` and a type check.
- REST endpoints with `permission_callback` (missing, or `__return_true` on an endpoint that modifies).

## Multi-tenant (Turso, Drizzle)

- Tenant filter in **every** query, including service queries and jobs.
- Tenant ID never taken from the client without verification on the server.

## Go, Cloud Run, Terraform

- Service accounts with least privilege; ingress declared.
- Secrets from Secret Manager, never in plain variables in the Terraform or in the image.
- Buckets not public.
- `google_cloud_run_v2_*` resources checked by hand: which IaC scanners cover them has not been measured, so no scanner is relied on for these.

## Apps with LLMs and agents (for example MCP servers)

- External text (pages, documents, API responses, users' messages) that reaches the prompt: it is untrusted input.
- Tools with write permissions: read-only by default, validation at the boundary of the action, refusal when in doubt.
- Model output used as a command, query, URL or HTML.
- PII in logs and traces.

## GDPR (for whoever has no privacy-audit subagent)

- Which personal data come in, towards which third parties they go out, where they are stored, on which legal basis.
- Third-party trackers and scripts loaded **before** consent: they are the main data point, with destination URL and cookies set.
- Customers' or users' data in logs, in error messages, in analytics tools.

## Secrets and configuration (always)

- Keys, tokens, passwords in the diff, in example files, in tests, in comments: by file, line and type, **never the value**.
- Silent fallbacks that open: a check that lets things through on error, a default secret in the code, debug active in production, default credentials, broad permissions.
