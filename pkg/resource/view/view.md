Use the *clickhousedbops_view* resource to manage ClickHouse views created with `CREATE VIEW`.

The `columns` attribute is optional and can be shared from Terraform locals in the same way as table column definitions. ClickHouse stores an inferred column list for every view. When `columns` is not set, that list is not tracked.

A change of `query` or `columns` is applied in place with `CREATE OR REPLACE VIEW`.

Set `node` to manage the view on one server: the provider connects to `node.host` with its own protocol, credentials and TLS settings, and runs the DDL there without `ON CLUSTER`. To put the view on several servers, declare one resource per server, for example with `for_each` over your node list. Without `node`, the view lives on the provider's host.

- `node.name` identifies the server: changing it replaces the view. `node.host` is only its address: changing it reconnects without a replacement.
- Import with `database.name@<node name>@<host>[:<port>]` so that the imported state names the node.

Creating a view that already exists is an error, unless the provider sets `adopt_existing = true`. With adoption the existing view is changed in place to match the configuration, or left alone when it already matches.

SQL text is compared with ClickHouse ignoring whitespace and line breaks outside quotes, so an attribute can be written over several lines. Apart from that, write each attribute the way ClickHouse prints it in `SHOW CREATE`, because ClickHouse rewrites expressions into its canonical form.
