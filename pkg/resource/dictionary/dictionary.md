Use the *clickhousedbops_dictionary* resource to manage ClickHouse dictionaries created with `CREATE DICTIONARY`.

The `attributes` block is a list of objects so you can define a single local value and reuse it across dictionaries and the tables they read from. An attribute type can contain `Nullable(...)` verbatim, or you can set `nullable = true`: both forms are equal.

A change of any attribute except `database`, `name` and `cluster_name` is applied in place with `CREATE OR REPLACE DICTIONARY`.

ClickHouse reports `PASSWORD '[HIDDEN]'` in the `SOURCE` clause. The provider compares `source` with the password value masked and keeps the configured text in state, so a change of only the password is not detected.

When the provider sets `fanout_cluster`, the resource acts on every node of that cluster and never uses `ON CLUSTER`:

- Create and update make every node hold the configured definition. A missing dictionary is created. A dictionary that differs is replaced in place with `CREATE OR REPLACE DICTIONARY`.
- Read queries every node. The `nodes` attribute lists the hosts that have the dictionary. When a node is missing the dictionary, or a node joins the cluster later, the plan shows an in-place update of `nodes` and the apply creates the dictionary there. When one node holds a different definition, the plan shows that difference.
- Delete drops the dictionary on every node with `DROP ... IF EXISTS ... SYNC`.
- `cluster_name` cannot be set together with `fanout_cluster`.

Creating a dictionary that already exists on a node is an error, unless the provider sets `adopt_existing = true`. With adoption the existing dictionary is changed in place to match the configuration, or left alone when it already matches.

SQL text is compared with ClickHouse ignoring whitespace and line breaks outside quotes, so an attribute can be written over several lines. Apart from that, write each attribute the way ClickHouse prints it in `SHOW CREATE`, because ClickHouse rewrites expressions into its canonical form.
