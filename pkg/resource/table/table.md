Use the *clickhousedbops_table* resource to manage ClickHouse objects created with `CREATE TABLE`, including local tables, distributed tables, Kafka tables, and other engine-backed table definitions.

The `columns` attribute is a list of objects so you can define a single local value and reuse it across multiple resources. A column type can contain `Nullable(...)` verbatim, or you can set `nullable = true`: both forms are equal. A column can also carry a `codec`, a column `ttl`, and one of `default_expression`, `materialized_expression`, `alias_expression` or `ephemeral_expression`.

The `indexes`, `projections` and `constraints` attributes are compared by name, not by position, because ClickHouse lists them in creation order.

Update behavior is engine-aware:

- MergeTree-family tables are updated in place for supported column changes (including `codec` and column `ttl`), indexes, projections, constraints, `SAMPLE BY`, `TTL`, append-only `ORDER BY` extensions that introduce newly-added columns in the same change, and mutable table settings. A changed index, projection or constraint is dropped and added again.
- Distributed tables are updated in place for column changes, but settings changes still force replacement.
- Kafka tables are treated as replacement-oriented for schema changes because ClickHouse does not support the necessary `ALTER TABLE` operations there.
- Engines without an explicit in-place strategy currently fall back to replacement for schema changes.

Changes that ClickHouse cannot alter safely in place, such as engine changes, `partition_by`, `primary_key`, `as_select`, unsupported `order_by` rewrites, readonly table settings, or removing `ephemeral_expression` from a column, still force replacement.

Column changes use `ADD COLUMN IF NOT EXISTS` and `DROP COLUMN IF EXISTS`. Every `ALTER TABLE` runs with `alter_sync = 0`, so it never waits for a mutation to rewrite data. The provider then waits until every replica has applied the new metadata, and watches the mutations the `ALTER` started for a few seconds: one that fails is an error, and one still running is a warning.

A change that drops the table, a destroy or a replacement, is refused while a MergeTree-family table holds rows on any node. The plan fails and names the nodes and their row counts. To drop the data on purpose, set `force_destroy = true` and apply that change on its own first: the value in state counts, so setting it in the same plan as the replacement is refused too.

ClickHouse refuses to drop a table that a dictionary or view reads from, so replacing such a table fails. With `ignore_drop_dependencies = true`, a change that cannot be altered in place recreates the table within the update instead: it drops the table with `check_table_dependencies = 0` and creates it again, and the plan shows an in-place update with a warning. The configured value counts, so this works in the same apply that imports or adopts the table. On this path `force_destroy` also counts from the configuration: a table that holds rows is recreated only when the configuration sets it. An existing `Replicated*` table can be recreated as a local table, because its drop releases the replica in Keeper; recreating it as a `Replicated*` table again is refused, because its replicas cannot be recreated one node at a time.

A mutation that keeps failing on the table fails every plan of it, with the `KILL MUTATION` statement that stops it. Its `ALTER` already changed the metadata, so without this the table would look up to date while its data is not.

`unmanaged_columns` and `unmanaged_indexes` are lists of RE2 regular expressions. A remote column or index whose name matches a pattern and that the configuration does not declare is invisible to the provider: it is not reported, changed, or dropped.

Set `node` to manage the table on one server: the provider connects to `node.host` with its own protocol, credentials and TLS settings, and runs the DDL there without `ON CLUSTER`. To put the table on several servers, declare one resource per server, for example with `for_each` over your node list. Without `node`, the table lives on the provider's host.

- `node.name` identifies the server: changing it replaces the table. `node.host` is only its address: changing it reconnects without a replacement.
- Import with `database.name@<node name>@<host>[:<port>]` so that the imported state names the node.
- On a `Replicated*` table, set `replica_role` on each replica of a shard. The `leader` runs the `ALTER`s that ClickHouse replicates through Keeper; each `follower` waits until that metadata has arrived and then runs only `MODIFY SETTING` and `RESET SETTING`, which ClickHouse does not replicate. A follower that still differs fails instead of altering. Apply followers after their leader, for example with `depends_on`.

Creating a table that already exists is an error, unless the provider sets `adopt_existing = true`. With adoption the existing table is changed in place to match the configuration, or left alone when it already matches.

SQL text is compared with ClickHouse ignoring whitespace and line breaks outside quotes, so an attribute can be written over several lines. Apart from that, write each attribute the way ClickHouse prints it in `SHOW CREATE`, because ClickHouse rewrites expressions into its canonical form.
