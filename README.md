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

## Reviewing and enforcing SQL from a saved plan

Set `enforce_sql_plan = true` on the provider configuration that manages your schema:

```hcl
provider "clickhousedbops" {
  # Existing connection and authentication settings.
  enforce_sql_plan = true
}
```

1. Run `tofu plan -no-color -out=tfplan | tee reviewed-sql.txt` (or the equivalent Terraform command).
2. Review each **Reviewed SQL execution plan** diagnostic: it lists the SQL and connection target for every proposed write, including drops, replacements and staging-table cleanup.
3. Apply that exact artifact with `tofu apply tfplan`.

Planning uses the existing resource write paths and SQL builders, with writes recorded rather than sent to ClickHouse. The saved plan carries the full statements, connection targets and bound parameter values. A computed digest binds create/update statements to the planned state, so apply-time replanning cannot silently substitute different SQL. Destroy and replacement drops use the saved private manifest because their planned state is null.

Before sending a write, the client checks its SQL, target, bound values, count and order against that manifest. Missing manifests and unreviewed writes fail closed. Already satisfied writes may be skipped, including writes satisfied by replication. The guarantee is that the provider executes **only reviewed writes**, not that every listed write runs or that the operation is atomic: earlier reviewed writes can remain applied after a later failure. Read-only queries and ClickHouse's internal replication, mutations and `ON CLUSTER` execution are outside the client write manifest.

Terraform keeps its normal resource and attribute diff. SQL is a separate plan diagnostic, not a resource attribute or the basis of the diff. Capture `terraform plan -json -out=tfplan` (or `tofu plan`) to publish these diagnostics in a PR alongside `terraform show -no-color tfplan`. The JSON event stream contains each SQL report in `diagnostic.detail` when `diagnostic.summary` is `Reviewed SQL execution plan`; `terraform show` does not retain diagnostics. The computed `sql_plan_digest` binds the execution manifest to the saved plan.

This mode supports `table`, `view`, `materialized_view`, `dictionary` and `table_contents`, including resources that name a `node`. Changes to other resource types fail planning; put them on a separate provider configuration. All provider and resource configuration values must be known during planning. For `table_contents`, apply schema changes first: the target table and columns used by the partition key must already exist when planning the data. Nondeterministic inputs or changes to schema/topology can require a fresh plan.

Dictionary sources and bound parameter values are redacted in the printout. Their full values are still checked at apply and stored in the saved plan; protect the plan artifact like Terraform state. Use the same provider version for planning and applying.

## Migrating from terraform-provider-clickhouse

Please read the [Migration guide](https://github.com/ClickHouse/terraform-provider-clickhousedbops/blob/main/migrating/README.md)

## Development and contributing

Please read the [Development readme](https://github.com/ClickHouse/terraform-provider-clickhousedbops/blob/main/development/README.md)
