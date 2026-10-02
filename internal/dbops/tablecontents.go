package dbops

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"

	"github.com/pingcap/errors"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/clickhouseclient"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/querybuilder"
)

// Table contents are compared by a checksum that ClickHouse computes over the rows: the row count
// and the sum of a hash of every row. The sum does not depend on row order, and counts duplicates.
// The declared data is parsed by ClickHouse with the table's own column types, through the
// format() table function, so that the two checksums agree whenever the rows are the same.

var ErrTableNotFound = stderrors.New("table does not exist")

type insertableColumn struct {
	name string
	typ  string
}

// TableContentsChecksum returns the checksum of the rows the table holds on this node, and
// false when the table does not exist.
func (i *impl) TableContentsChecksum(ctx context.Context, database string, table string) (string, bool, error) {
	columns, err := i.insertableColumns(ctx, database, table)
	if stderrors.Is(err, ErrTableNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	checksum, err := i.checksum(ctx, qualified(database, table), columns, nil)
	return checksum, err == nil, err
}

// DataChecksum returns the checksum the table would have if it held exactly data, which is in
// the ClickHouse input format named by format.
func (i *impl) DataChecksum(ctx context.Context, database string, table string, format string, data string) (string, error) {
	columns, err := i.insertableColumns(ctx, database, table)
	if err != nil {
		return "", err
	}
	return i.checksum(ctx, formatSource(format, columns), columns, dataParam(data))
}

// ReplaceTableContents makes the table hold exactly data. The rows are loaded into a staging
// table first and swapped in with REPLACE PARTITION, one partition at a time, so that readers
// see either the old or the new rows of a partition and never an empty table. Partitions that
// the new data does not have are dropped last. An unpartitioned table is one swap.
func (i *impl) ReplaceTableContents(ctx context.Context, database string, table string, format string, data string) error {
	columns, err := i.insertableColumns(ctx, database, table)
	if err != nil {
		return err
	}

	definition, partitionKey, err := i.stagingDefinition(ctx, database, table)
	if err != nil {
		return err
	}
	staging := "_tf_contents_" + table
	target := qualified(database, table)
	stagingName := qualified(database, staging)

	if err := i.clickhouseClient.Exec(ctx, "DROP TABLE IF EXISTS "+stagingName+" SYNC"); err != nil {
		return errors.WithMessage(err, "error dropping a leftover staging table")
	}
	if err := i.clickhouseClient.Exec(ctx, fmt.Sprintf("CREATE TABLE %s AS %s ENGINE = %s", stagingName, target, definition)); err != nil {
		return errors.WithMessage(err, "error creating the staging table")
	}
	defer func() {
		_ = i.clickhouseClient.Exec(context.WithoutCancel(ctx), "DROP TABLE IF EXISTS "+stagingName+" SYNC")
	}()

	names := columnNames(columns)
	insert := fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s", stagingName, names, names, formatSource(format, columns))
	if err := i.clickhouseClient.Exec(ctx, insert, dataParam(data)); err != nil {
		return errors.WithMessage(err, "error loading the data into the staging table")
	}

	var newPartitions []string
	if clickhouseclient.IsRecordingSQL(ctx) {
		newPartitions, err = i.dataPartitionIDs(ctx, partitionKey, format, data, columns)
	} else {
		newPartitions, err = i.partitionIDs(ctx, database, staging)
	}
	if err != nil {
		return err
	}
	oldPartitions, err := i.partitionIDs(ctx, database, table)
	if err != nil {
		return err
	}
	// alter_sync = 2: the swap moves no data through a mutation, and waiting for every replica
	// to fetch the new parts is what makes a read on any replica see the new rows.
	for _, id := range newPartitions {
		sql := fmt.Sprintf("ALTER TABLE %s REPLACE PARTITION ID %s FROM %s SETTINGS alter_sync = 2", target, stringLiteral(id), stagingName)
		if err := i.clickhouseClient.Exec(ctx, sql); err != nil {
			return errors.WithMessage(err, "error swapping in partition "+id)
		}
	}
	keep := make(map[string]bool, len(newPartitions))
	for _, id := range newPartitions {
		keep[id] = true
	}
	for _, id := range oldPartitions {
		if keep[id] {
			continue
		}
		sql := fmt.Sprintf("ALTER TABLE %s DROP PARTITION ID %s SETTINGS alter_sync = 2", target, stringLiteral(id))
		if err := i.clickhouseClient.Exec(ctx, sql); err != nil {
			return errors.WithMessage(err, "error dropping partition "+id)
		}
	}
	return nil
}

// IsReplicated reports whether the table is a Replicated table, whose rows every replica of a
// shard shares.
func (i *impl) IsReplicated(ctx context.Context, database string, table string) (bool, error) {
	path, err := i.ReplicationPath(ctx, database, table)
	return path != "", err
}

func (i *impl) checksum(ctx context.Context, source string, columns []insertableColumn, params map[string]string) (string, error) {
	// The row is hashed as one tuple: cityHash64 of separate Nullable arguments is NULL when any of
	// them is, and sum() would skip those rows.
	sql := fmt.Sprintf("SELECT toUInt64(count()) AS rows, toUInt64(sum(cityHash64(tuple(%s)))) AS hash FROM %s", columnNames(columns), source)
	var rows, hash uint64
	callback := func(data clickhouseclient.Row) error {
		var err error
		if rows, err = data.GetUInt64("rows"); err != nil {
			return err
		}
		hash, err = data.GetUInt64("hash")
		return err
	}
	var err error
	if params != nil {
		err = i.clickhouseClient.Select(ctx, sql, callback, params)
	} else {
		err = i.clickhouseClient.Select(ctx, sql, callback)
	}
	if err != nil {
		return "", errors.WithMessage(err, "error computing the contents checksum")
	}
	return fmt.Sprintf("%d:%016x", rows, hash), nil
}

// insertableColumns are the columns a row of data sets: every column but MATERIALIZED, ALIAS
// and EPHEMERAL ones, in table order.
func (i *impl) insertableColumns(ctx context.Context, database string, table string) ([]insertableColumn, error) {
	sql := fmt.Sprintf(
		"SELECT name, type FROM system.columns WHERE database = %s AND table = %s AND default_kind NOT IN ('MATERIALIZED', 'ALIAS', 'EPHEMERAL') ORDER BY position",
		stringLiteral(database), stringLiteral(table))
	var columns []insertableColumn
	err := i.clickhouseClient.Select(ctx, sql, func(data clickhouseclient.Row) error {
		name, err := data.GetString("name")
		if err != nil {
			return err
		}
		typ, err := data.GetString("type")
		columns = append(columns, insertableColumn{name: name, typ: typ})
		return err
	})
	if err != nil {
		return nil, errors.WithMessage(err, "error reading the table's columns")
	}
	if len(columns) == 0 {
		exists := false
		sql := fmt.Sprintf("SELECT name FROM system.tables WHERE database = %s AND name = %s", stringLiteral(database), stringLiteral(table))
		if err := i.clickhouseClient.Select(ctx, sql, func(clickhouseclient.Row) error { exists = true; return nil }); err != nil {
			return nil, errors.WithMessage(err, "error checking whether the table exists")
		}
		if !exists {
			return nil, fmt.Errorf("table %s.%s: %w", database, table, ErrTableNotFound)
		}
		return nil, errors.Errorf("table %s.%s has no insertable columns", database, table)
	}
	return columns, nil
}

// stagingDefinition is the engine clause of a non-replicated MergeTree table that REPLACE
// PARTITION accepts as a source for the table: the same partition key, sorting key, primary
// key and storage policy.
func (i *impl) stagingDefinition(ctx context.Context, database string, table string) (string, string, error) {
	sql := fmt.Sprintf(
		"SELECT engine, partition_key, sorting_key, primary_key, storage_policy FROM system.tables WHERE database = %s AND name = %s",
		stringLiteral(database), stringLiteral(table))
	var engine, partitionKey, sortingKey, primaryKey, storagePolicy string
	err := i.clickhouseClient.Select(ctx, sql, func(data clickhouseclient.Row) error {
		var err error
		for field, target := range map[string]*string{"engine": &engine, "partition_key": &partitionKey, "sorting_key": &sortingKey, "primary_key": &primaryKey, "storage_policy": &storagePolicy} {
			if *target, err = data.GetString(field); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", "", errors.WithMessage(err, "error reading the table's keys")
	}
	// Other MergeTree variants merge rows by rules the staging table would have to repeat.
	if engine != "MergeTree" && engine != "ReplicatedMergeTree" {
		return "", "", errors.Errorf("table %s.%s has engine %s; declared contents need MergeTree or ReplicatedMergeTree", database, table, engine)
	}

	definition := "MergeTree"
	if partitionKey != "" {
		definition += " PARTITION BY " + keyExpression(partitionKey)
	}
	if sortingKey == "" {
		definition += " ORDER BY tuple()"
	} else {
		definition += " ORDER BY " + keyExpression(sortingKey)
	}
	if primaryKey != "" && primaryKey != sortingKey {
		definition += " PRIMARY KEY " + keyExpression(primaryKey)
	}
	if storagePolicy != "" {
		definition += " SETTINGS storage_policy = " + stringLiteral(storagePolicy)
	}
	return definition, partitionKey, nil
}

// Planning reads the input through format(), without creating or populating staging tables.
func (i *impl) dataPartitionIDs(ctx context.Context, partitionKey, format, data string, columns []insertableColumn) ([]string, error) {
	expression := "'all'"
	if partitionKey != "" && partitionKey != "tuple()" {
		// MergeTree flattens a top-level tuple into separate partition-key columns.
		open, close, found, err := querybuilder.FindTrailingTopLevelParentheses(partitionKey)
		if err != nil {
			return nil, err
		}
		if found && open == 0 && close == len(partitionKey)-1 {
			partitionKey = partitionKey[1:close]
		}
		expression = "partitionId(" + partitionKey + ")"
	}
	sql := fmt.Sprintf("SELECT DISTINCT %s AS partition_id FROM %s ORDER BY partition_id", expression, formatSource(format, columns))
	var ids []string
	err := i.clickhouseClient.Select(ctx, sql, func(row clickhouseclient.Row) error {
		id, err := row.GetString("partition_id")
		ids = append(ids, id)
		return err
	}, dataParam(data))
	if err != nil {
		return nil, errors.WithMessage(err, "cannot review input partition IDs; the target table and its partition-key columns must exist at plan time")
	}
	return ids, nil
}

func (i *impl) partitionIDs(ctx context.Context, database string, table string) ([]string, error) {
	sql := fmt.Sprintf(
		"SELECT DISTINCT partition_id FROM system.parts WHERE database = %s AND table = %s AND active ORDER BY partition_id",
		stringLiteral(database), stringLiteral(table))
	var ids []string
	err := i.clickhouseClient.Select(ctx, sql, func(data clickhouseclient.Row) error {
		id, err := data.GetString("partition_id")
		ids = append(ids, id)
		return err
	})
	if err != nil {
		return nil, errors.WithMessage(err, "error listing partitions")
	}
	return ids, nil
}

// keyExpression turns a key as system.tables lists it back into the clause text the table was
// created with. REPLACE PARTITION compares the keys as written, so `k` and `(k)` differ, and a
// key of several expressions is listed without the parentheses of its tuple.
func keyExpression(key string) string {
	depth, quoted := 0, rune(0)
	for _, ch := range key {
		switch {
		case quoted != 0:
			if ch == quoted {
				quoted = 0
			}
		case ch == '\'' || ch == '`' || ch == '"':
			quoted = ch
		case ch == '(' || ch == '[':
			depth++
		case ch == ')' || ch == ']':
			depth--
		case ch == ',' && depth == 0:
			return "(" + key + ")"
		}
	}
	return key
}

func formatSource(format string, columns []insertableColumn) string {
	structure := make([]string, 0, len(columns))
	for _, column := range columns {
		structure = append(structure, identifier(column.name)+" "+column.typ)
	}
	return fmt.Sprintf("format(%s, %s, {data:String})", identifier(format), stringLiteral(strings.Join(structure, ", ")))
}

// dataParam passes the data as a query parameter, outside the query text, so max_query_size
// does not limit it. ClickHouse reads String parameters in escaped form.
func dataParam(data string) map[string]string {
	escaped := strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\t", `\t`, "\r", `\r`).Replace(data)
	return map[string]string{"data": escaped}
}

func columnNames(columns []insertableColumn) string {
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		names = append(names, identifier(column.name))
	}
	return strings.Join(names, ", ")
}

func qualified(database string, name string) string {
	return identifier(database) + "." + identifier(name)
}

func identifier(name string) string {
	return "`" + strings.ReplaceAll(strings.ReplaceAll(name, `\`, `\\`), "`", "\\`") + "`"
}

func stringLiteral(value string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), "'", `\'`) + "'"
}
