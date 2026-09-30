Use the *clickhousedbops_materialized_view* resource to manage ClickHouse materialized views created with `CREATE MATERIALIZED VIEW`.

You can either define a destination `to_table` or an inline `engine`, and `to_columns` lets you reuse the same shared column list across the target table and the materialized view definition. ClickHouse stores an inferred column list for every materialized view. When `to_columns` (or `columns` for an engine-backed materialized view) is not set, that list is not tracked.

Update behavior:

- With `to_table`, a change of `query` is applied in place with `ALTER TABLE ... MODIFY QUERY`. When the resource sets `cluster_name`, a change of `query` replaces the materialized view.
- Every other change, and every change of an engine-backed materialized view, replaces the materialized view.

When the provider sets `fanout_cluster`, the resource acts on every node of that cluster and never uses `ON CLUSTER`:

- Create and update make every node hold the configured definition. A missing materialized view is created. For an existing one only the query of a TO-table materialized view is changed in place; when another attribute differs, the apply fails with an error that names the node and the attributes.
- Read queries every node. The `nodes` attribute lists the hosts that have the materialized view. When a node is missing the materialized view, or a node joins the cluster later, the plan shows an in-place update of `nodes` and the apply creates the materialized view there. When one node holds a different definition, the plan shows that difference.
- Delete drops the materialized view on every node with `DROP ... IF EXISTS ... SYNC`.
- `cluster_name` cannot be set together with `fanout_cluster`.

Creating a materialized view that already exists on a node is an error, unless the provider sets `adopt_existing = true`. With adoption the existing materialized view is changed in place to match the configuration, or left alone when it already matches.

SQL text is compared with ClickHouse ignoring whitespace and line breaks outside quotes, so an attribute can be written over several lines. Apart from that, write each attribute the way ClickHouse prints it in `SHOW CREATE`, because ClickHouse rewrites expressions into its canonical form.
