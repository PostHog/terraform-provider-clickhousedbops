Use the *clickhousedbops_materialized_view* resource to manage ClickHouse materialized views created with `CREATE MATERIALIZED VIEW`.

You can either define a destination `to_table` or an inline `engine`, and `to_columns` lets you reuse the same shared column list across the target table and the materialized view definition. ClickHouse stores an inferred column list for every materialized view. When `to_columns` (or `columns` for an engine-backed materialized view) is not set, that list is not tracked.

Update behavior:

- With `to_table`, a change of `query` is applied in place with `ALTER TABLE ... MODIFY QUERY`. When the resource sets `cluster_name`, a change of `query` replaces the materialized view.
- Every other change, and every change of an engine-backed materialized view, replaces the materialized view.

Set `node` to manage the materialized view on one server: the provider connects to `node.host` with its own protocol, credentials and TLS settings, and runs the DDL there without `ON CLUSTER`. To put the materialized view on several servers, declare one resource per server, for example with `for_each` over your node list. Without `node`, the materialized view lives on the provider's host.

- `node.name` identifies the server: changing it replaces the materialized view. `node.host` is only its address: changing it reconnects without a replacement.
- Import with `database.name@<node name>@<host>[:<port>]` so that the imported state names the node.

Creating a materialized view that already exists is an error, unless the provider sets `adopt_existing = true`. With adoption the existing materialized view is changed in place to match the configuration, or left alone when it already matches.

SQL text is compared with ClickHouse ignoring whitespace and line breaks outside quotes, so an attribute can be written over several lines. Apart from that, write each attribute the way ClickHouse prints it in `SHOW CREATE`, because ClickHouse rewrites expressions into its canonical form.
