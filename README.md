# Clickhouse DB ops Terraform Provider

[![PostHog Release](https://github.com/PostHog/terraform-provider-clickhousedbops/actions/workflows/posthog-release.yaml/badge.svg)](https://github.com/PostHog/terraform-provider-clickhousedbops/actions/workflows/posthog-release.yaml)

This is PostHog's fork of the Terraform provider for ClickHouse database operations.

Every push to `main`, including merged pull requests, publishes the next patch version to
[PostHog's GitHub releases](https://github.com/PostHog/terraform-provider-clickhousedbops/releases)
after the Go tests and builds pass. The existing `.goreleaser.yml` builds and publishes the
provider binaries, SHA256 checksums, and Terraform protocol manifest using the built-in
`GITHUB_TOKEN`. The workflow uses GitHub-hosted runners and skips GPG signing, so it needs no
upstream runners or signing secrets. Releases go to this fork's GitHub releases; they are
unsigned and do not publish to the upstream Terraform Registry provider.

Inherited workflows and their unused local actions are removed; their previous definitions
remain available in Git history.

With this Terraform provider you can:

- Manage `databases` in a `ClickHouse` instance using the `clickhousedbops_database` resource
- Manage `dictionaries` in a `ClickHouse` instance using the `clickhousedbops_dictionary` resource
- Manage `tables` in a `ClickHouse` instance using the `clickhousedbops_table` resource
- Declare every row of a small reference table using the `clickhousedbops_table_contents` resource
- Manage `views` in a `ClickHouse` instance using the `clickhousedbops_view` resource
- Manage `materialized views` in a `ClickHouse` instance using the `clickhousedbops_materialized_view` resource
- Manage `users` in a `ClickHouse` instance using the `clickhousedbops_user` resource
- Manage `roles` in a `ClickHouse` instance using the `clickhousedbops_role` resource
- Manage `role grants` in a `ClickHouse` instance using the `clickhousedbops_grant_role` resource
- Manage `privilege grants` in a `ClickHouse` instance using the `clickhousedbops_grant_privilege` resource

## Getting started

The `clickhousedbops_user` resource works with both Terraform and OpenTofu. Write-only authentication values (the `auth` block's `value_wo` fields and the legacy `password_sha256_hash_wo`) require at least Terraform 1.11 (write-only arguments support); the in-state `value` / `password_sha256_hash` fields work with all versions. All other resources work with older versions too.

You can find examples in the [examples/tests](https://github.com/ClickHouse/terraform-provider-clickhousedbops/tree/main/examples/tests) directory.

Please refer to the [official docs](https://registry.terraform.io/providers/ClickHouse/clickhousedbops/latest/docs) for more details.

## Migrating from terraform-provider-clickhouse

Please read the [Migration guide](https://github.com/ClickHouse/terraform-provider-clickhousedbops/blob/main/migrating/README.md)

## Development and contributing

Please read the [Development readme](https://github.com/ClickHouse/terraform-provider-clickhousedbops/blob/main/development/README.md)
