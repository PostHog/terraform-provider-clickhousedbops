# Development

Quick start guide for contributors.

## Preparation

Create a new file called .terraformrc in your home directory (~), then add the dev_overrides block below. Change the `<PATH>` to the full path of the `tmp` directory in this repo. For example:

```t
provider_installation {

  dev_overrides {
      "ClickHouse/clickhousedbops" = "<PATH example /home/user/workdir/terraform-provider-clickhousedbops/tmp>"
  }

  # For all other providers, install them directly from their origin provider
  # registries as normal. If you omit this, Terraform will _only_ use
  # the dev_overrides block, and so no other providers will be available.
  direct {}
}
```

Ensure you have [`air`](https://github.com/air-verse/air) or install it with:

```bash
go install github.com/air-verse/air@latest
```

Run `air` to automatically build the plugin binary every time you make changes to the code:

```bash
$ air
```

You can now run `terraform` and you'll be using the locally built binary. Please note that the `dev_overrides` make it so that you have to skip `terraform init`).

## Git hooks

We suggest to add git hooks to your local repo, by running:

```bash
make enable_git_hooks
```

Code will be formatted and docs generated before each commit.

## Docs

If you made any changes to the provider's interface, please run `make docs` to update documentation as well.

NOTE: this is done automatically by git hooks.

## Local schema tests

The schema-management resources use the existing acceptance-test ClickHouse harness under [`tests/docker-compose.yaml`](../tests/docker-compose.yaml). Run the new local tests with:

```bash
make tftest RESOURCE=dictionary
make tftest RESOURCE=table
make tftest RESOURCE=view
make tftest RESOURCE=materializedview
```

These tests exercise shared Terraform locals across managed databases, dictionaries, tables, views, and materialized views.

Kafka tables are supported by the generic `clickhousedbops_table` resource because the engine clause is raw ClickHouse SQL. The automated acceptance suite currently validates that through DDL builder unit tests; if you want a full end-to-end Kafka check locally, add a broker to the compose stack and point the table engine at that broker.

## Saved SQL plan integration test

The process-level test builds the provider, runs separate plan/apply CLI processes, and uses a temporary database that it removes afterwards:

```bash
SQL_PLAN_TEST_CLICKHOUSE_URL=http://localhost:8123 \
TF_ACC_TERRAFORM_PATH=/path/to/tofu \
go test ./pkg/provider -run TestSQLPlanSavedFile -v -count=1
```

Use a disposable ClickHouse instance with the `default` user and no password. Set `TF_ACC_TERRAFORM_PATH` to Terraform to run the same saved-plan checks with that CLI.

The test checks that planning sends no writes, SQL appears in the printout, config edits cannot change a saved apply, schema drift fails before writing, and create/update/replacement/destroy and declared contents work. It also checks partition IDs against actual ClickHouse parts, including composite and empty partition keys.

## Release

NOTE: Release process is only possible for ClickHouse employees.

To make a new public release:

- ensure the `main` branch contains all the changes you want to release
- Run the [`Release`](https://github.com/ClickHouse/terraform-provider-clickhousedbops/actions/workflows/release.yaml) workflow against the main branch (enter the desired release version in semver format without leading `v`, example: "1.2.3")
- Release will be automatically created if end to end tests will be successful.
