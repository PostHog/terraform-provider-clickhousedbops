Use the *clickhousedbops_dictionary* resource to manage ClickHouse dictionaries created with `CREATE DICTIONARY`.

The `attributes` block is a list of objects so you can define a single local value and reuse it across dictionaries and the tables they read from. An attribute type can contain `Nullable(...)` verbatim, or you can set `nullable = true`: both forms are equal.

A change of any attribute except `database`, `name` and `cluster_name` is applied in place with `CREATE OR REPLACE DICTIONARY`.

ClickHouse reports `PASSWORD '[HIDDEN]'` in the `SOURCE` clause. The provider compares `source` with the password value masked and keeps the configured text in state, so a change of only the password is not detected.

Set `node` to manage the dictionary on one server: the provider connects to `node.host` with its own protocol, credentials and TLS settings, and runs the DDL there without `ON CLUSTER`. To put the dictionary on several servers, declare one resource per server, for example with `for_each` over your node list. Without `node`, the dictionary lives on the provider's host.

- `node.name` identifies the server: changing it replaces the dictionary. `node.host` is only its address: changing it reconnects without a replacement.
- Import with `database.name@<node name>@<host>[:<port>]` so that the imported state names the node.

Creating a dictionary that already exists is an error, unless the provider sets `adopt_existing = true`. With adoption the existing dictionary is changed in place to match the configuration, or left alone when it already matches.

SQL text is compared with ClickHouse ignoring whitespace and line breaks outside quotes, so an attribute can be written over several lines. Apart from that, write each attribute the way ClickHouse prints it in `SHOW CREATE`, because ClickHouse rewrites expressions into its canonical form.
