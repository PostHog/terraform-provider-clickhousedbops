Declares every row of a table, for small reference tables whose rows live in a file in the repository.

After an apply the table holds exactly the rows in `data`, on every node. The plan compares a checksum of the rows: ClickHouse parses `data` with the table's own column types, so a file that formats the same rows differently is not a change, and a row someone inserted or deleted by hand is.

The rows are loaded into a staging table and swapped in with `REPLACE PARTITION`, one partition at a time, so readers never see an empty table. An unpartitioned table is one atomic swap. Partitions that `data` no longer has are dropped last.

Removing the resource leaves the rows in place.

The table must use `MergeTree` or `ReplicatedMergeTree`: other engines merge or deduplicate rows, so the table would not hold what `data` says.
