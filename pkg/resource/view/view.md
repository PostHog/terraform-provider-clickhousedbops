Use the *clickhousedbops_view* resource to manage ClickHouse views created with `CREATE VIEW`.

The `columns` attribute is optional and can be shared from Terraform locals in the same way as table column definitions. ClickHouse stores an inferred column list for every view. When `columns` is not set, that list is not tracked.

A change of `query` or `columns` is applied in place with `CREATE OR REPLACE VIEW`.

When the provider sets `fanout_cluster`, the resource acts on every node of that cluster and never uses `ON CLUSTER`:

- Create and update make every node hold the configured definition. A missing view is created. A view that differs is replaced in place with `CREATE OR REPLACE VIEW`.
- Read queries every node. The `nodes` attribute lists the hosts that have the view. When a node is missing the view, or a node joins the cluster later, the plan shows an in-place update of `nodes` and the apply creates the view there. When one node holds a different definition, the plan shows that difference.
- Delete drops the view on every node with `DROP ... IF EXISTS ... SYNC`.
- `cluster_name` cannot be set together with `fanout_cluster`.

Creating a view that already exists on a node is an error, unless the provider sets `adopt_existing = true`. With adoption the existing view is changed in place to match the configuration, or left alone when it already matches.

SQL text is compared with ClickHouse ignoring whitespace and line breaks outside quotes, so an attribute can be written over several lines. Apart from that, write each attribute the way ClickHouse prints it in `SHOW CREATE`, because ClickHouse rewrites expressions into its canonical form.
