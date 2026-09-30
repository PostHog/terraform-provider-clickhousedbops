package dbops

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pingcap/errors"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/clickhouseclient"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/querybuilder"
)

type Column struct {
	Name                   string
	Type                   string
	Nullable               bool
	Comment                string
	DefaultExpression      *string
	MaterializedExpression *string
	AliasExpression        *string
	EphemeralExpression    *string
	Codec                  string
	TTL                    string
}

type (
	Index      = querybuilder.IndexDefinition
	Projection = querybuilder.ProjectionDefinition
	Constraint = querybuilder.ConstraintDefinition
)

type Table struct {
	Database        string
	Name            string
	Engine          string
	Columns         []Column
	Indexes         []Index
	Projections     []Projection
	Constraints     []Constraint
	PartitionBy     string
	OrderBy         string
	PrimaryKey      string
	SampleBy        string
	TTL             string
	Settings        string
	AsSelect        string
	CreateStatement string
}

type View struct {
	Database        string
	Name            string
	Columns         []Column
	Query           string
	CreateStatement string
}

type MaterializedView struct {
	Database        string
	Name            string
	Columns         []Column
	Engine          string
	PartitionBy     string
	OrderBy         string
	PrimaryKey      string
	SampleBy        string
	TTL             string
	Settings        string
	Populate        bool
	ToTable         string
	ToColumns       []Column
	Query           string
	CreateStatement string
}

type schemaObject struct {
	Database        string
	Name            string
	Engine          string
	EngineFull      string
	CreateStatement string
}

type schemaObjectKind string

const (
	schemaObjectKindTable            schemaObjectKind = "table"
	schemaObjectKindView             schemaObjectKind = "view"
	schemaObjectKindMaterializedView schemaObjectKind = "materialized_view"
)

func (i *impl) CreateTable(ctx context.Context, table Table, clusterName *string) (*Table, error) {
	sql, err := querybuilder.CreateTableQuery{
		Database:    table.Database,
		Name:        table.Name,
		ClusterName: clusterName,
		Columns:     toQueryBuilderColumns(table.Columns),
		Indexes:     table.Indexes,
		Projections: table.Projections,
		Constraints: table.Constraints,
		Engine:      table.Engine,
		PartitionBy: table.PartitionBy,
		OrderBy:     table.OrderBy,
		PrimaryKey:  table.PrimaryKey,
		SampleBy:    table.SampleBy,
		TTL:         table.TTL,
		Settings:    table.Settings,
		AsSelect:    table.AsSelect,
	}.Build()
	if err != nil {
		return nil, errors.WithMessage(err, "error building query")
	}

	if err := i.clickhouseClient.Exec(ctx, sql); err != nil {
		return nil, errors.WithMessage(err, "error running query")
	}

	return i.GetTable(ctx, table.Database, table.Name, clusterName)
}

func (i *impl) GetTable(ctx context.Context, database string, name string, clusterName *string) (*Table, error) {
	object, err := i.getSchemaObject(ctx, database, name, clusterName, schemaObjectKindTable)
	if err != nil {
		return nil, err
	}
	if object == nil {
		return nil, nil
	}

	elements, err := parseCreateTableElements(object.CreateStatement)
	if err != nil {
		return nil, errors.WithMessage(err, "error parsing column list of CREATE TABLE statement")
	}
	definition, err := parseCreateTableDefinition(object.CreateStatement)
	if err != nil {
		return nil, err
	}

	engine := definition.Engine
	if strings.TrimSpace(engine) == "" {
		engine = object.EngineFull
		if strings.TrimSpace(engine) == "" {
			engine = object.Engine
		}
	}

	return &Table{
		Database:        object.Database,
		Name:            object.Name,
		Engine:          engine,
		Columns:         elements.Columns,
		Indexes:         elements.Indexes,
		Projections:     elements.Projections,
		Constraints:     elements.Constraints,
		PartitionBy:     definition.PartitionBy,
		OrderBy:         definition.OrderBy,
		PrimaryKey:      definition.PrimaryKey,
		SampleBy:        definition.SampleBy,
		TTL:             definition.TTL,
		Settings:        definition.Settings,
		AsSelect:        definition.AsSelect,
		CreateStatement: object.CreateStatement,
	}, nil
}

func (i *impl) deleteIfExists(ctx context.Context, exists bool, dropBuilder querybuilder.QueryBuilder) error {
	if !exists {
		return nil
	}

	sql, err := dropBuilder.Build()
	if err != nil {
		return errors.WithMessage(err, "error building query")
	}

	if err := i.clickhouseClient.Exec(ctx, sql); err != nil {
		return errors.WithMessage(err, "error running query")
	}

	return nil
}

// DeleteTable drops the table. With skipDependencyCheck it drops it even while a dictionary or
// view reads from it, which a replacement needs: the table is created again right after.
func (i *impl) DeleteTable(ctx context.Context, database string, name string, clusterName *string, skipDependencyCheck bool) error {
	table, err := i.GetTable(ctx, database, name, clusterName)
	if err != nil {
		return err
	}
	return i.deleteIfExists(ctx, table != nil, querybuilder.NewDropTable(database, name).WithCluster(clusterName).SkipDependencyCheck(skipDependencyCheck))
}

// TableRows returns the rows the table holds on this node; 0 when it does not exist.
func (i *impl) TableRows(ctx context.Context, database string, name string) (uint64, error) {
	sql, err := querybuilder.NewSelect(
		[]querybuilder.Field{querybuilder.NewRawField("toUInt64(ifNull(total_rows, 0))", "rows")},
		"system.tables",
	).Where(querybuilder.WhereEquals("database", database), querybuilder.WhereEquals("name", name)).Build()
	if err != nil {
		return 0, errors.WithMessage(err, "error building query")
	}

	var rows uint64
	err = i.clickhouseClient.Select(ctx, sql, func(data clickhouseclient.Row) error {
		rows, err = data.GetUInt64("rows")
		return err
	})
	if err != nil {
		return 0, errors.WithMessage(err, "error reading the table's row count")
	}
	return rows, nil
}

// AlterTable runs the ALTER without waiting for the mutations it starts, then waits for the
// metadata change to reach every replica. It returns the mutations that are still running.
func (i *impl) AlterTable(ctx context.Context, database string, name string, clusterName *string, actions []string) ([]RunningMutation, error) {
	sql, err := querybuilder.BuildAlterTable(database, name, clusterName, actions)
	if err != nil {
		return nil, errors.WithMessage(err, "error building query")
	}

	startedAt, err := i.serverNow(ctx)
	if err != nil {
		return nil, err
	}
	if err := i.clickhouseClient.Exec(ctx, sql); err != nil {
		return nil, errors.WithMessage(err, "error running query")
	}

	return i.waitForAlter(ctx, database, name, startedAt)
}

func (i *impl) CreateView(ctx context.Context, view View, clusterName *string) (*View, error) {
	return i.createView(ctx, view, clusterName, false)
}

func (i *impl) ReplaceView(ctx context.Context, view View, clusterName *string) (*View, error) {
	return i.createView(ctx, view, clusterName, true)
}

func (i *impl) createView(ctx context.Context, view View, clusterName *string, orReplace bool) (*View, error) {
	sql, err := querybuilder.CreateViewQuery{
		Database:    view.Database,
		Name:        view.Name,
		ClusterName: clusterName,
		OrReplace:   orReplace,
		Columns:     toQueryBuilderColumns(view.Columns),
		Query:       view.Query,
	}.Build()
	if err != nil {
		return nil, errors.WithMessage(err, "error building query")
	}

	if err := i.clickhouseClient.Exec(ctx, sql); err != nil {
		return nil, errors.WithMessage(err, "error running query")
	}

	return i.GetView(ctx, view.Database, view.Name, clusterName)
}

func (i *impl) GetView(ctx context.Context, database string, name string, clusterName *string) (*View, error) {
	object, err := i.getSchemaObject(ctx, database, name, clusterName, schemaObjectKindView)
	if err != nil {
		return nil, err
	}
	if object == nil {
		return nil, nil
	}

	definition, err := parseCreateViewDefinition(object.CreateStatement)
	if err != nil {
		return nil, err
	}

	return &View{
		Database:        object.Database,
		Name:            object.Name,
		Columns:         definition.Columns,
		Query:           definition.Query,
		CreateStatement: object.CreateStatement,
	}, nil
}

func (i *impl) DeleteView(ctx context.Context, database string, name string, clusterName *string) error {
	view, err := i.GetView(ctx, database, name, clusterName)
	if err != nil {
		return err
	}
	return i.deleteIfExists(ctx, view != nil, querybuilder.NewDropView(database, name).WithCluster(clusterName))
}

func (i *impl) CreateMaterializedView(ctx context.Context, view MaterializedView, clusterName *string) (*MaterializedView, error) {
	sql, err := querybuilder.CreateMaterializedViewQuery{
		Database:    view.Database,
		Name:        view.Name,
		ClusterName: clusterName,
		Columns:     toQueryBuilderColumns(view.Columns),
		Engine:      view.Engine,
		PartitionBy: view.PartitionBy,
		OrderBy:     view.OrderBy,
		PrimaryKey:  view.PrimaryKey,
		SampleBy:    view.SampleBy,
		TTL:         view.TTL,
		Settings:    view.Settings,
		Populate:    view.Populate,
		ToTable:     view.ToTable,
		ToColumns:   toQueryBuilderColumns(view.ToColumns),
		Query:       view.Query,
	}.Build()
	if err != nil {
		return nil, errors.WithMessage(err, "error building query")
	}

	if err := i.clickhouseClient.Exec(ctx, sql); err != nil {
		return nil, errors.WithMessage(err, "error running query")
	}

	return i.GetMaterializedView(ctx, view.Database, view.Name, clusterName)
}

func (i *impl) GetMaterializedView(ctx context.Context, database string, name string, clusterName *string) (*MaterializedView, error) {
	object, err := i.getSchemaObject(ctx, database, name, clusterName, schemaObjectKindMaterializedView)
	if err != nil {
		return nil, err
	}
	if object == nil {
		return nil, nil
	}

	definition, err := parseCreateMaterializedViewDefinition(object.CreateStatement)
	if err != nil {
		return nil, errors.WithMessage(err, "error parsing CREATE MATERIALIZED VIEW statement")
	}

	mv := &MaterializedView{
		Database:        object.Database,
		Name:            object.Name,
		Engine:          definition.Engine,
		PartitionBy:     definition.PartitionBy,
		OrderBy:         definition.OrderBy,
		PrimaryKey:      definition.PrimaryKey,
		SampleBy:        definition.SampleBy,
		TTL:             definition.TTL,
		Settings:        definition.Settings,
		Populate:        definition.Populate,
		ToTable:         definition.ToTable,
		Columns:         definition.Columns,
		ToColumns:       definition.ToColumns,
		Query:           definition.Query,
		CreateStatement: object.CreateStatement,
	}

	return mv, nil
}

func (i *impl) ModifyMaterializedViewQuery(ctx context.Context, database string, name string, query string) error {
	sql, err := querybuilder.BuildModifyQuery(database, name, query)
	if err != nil {
		return errors.WithMessage(err, "error building query")
	}

	if err := i.clickhouseClient.Exec(ctx, sql); err != nil {
		return errors.WithMessage(err, "error running query")
	}

	return nil
}

func (i *impl) DeleteMaterializedView(ctx context.Context, database string, name string, clusterName *string) error {
	view, err := i.GetMaterializedView(ctx, database, name, clusterName)
	if err != nil {
		return err
	}
	return i.deleteIfExists(ctx, view != nil, querybuilder.NewDropMaterializedView(database, name).WithCluster(clusterName))
}

func (i *impl) getSchemaObject(ctx context.Context, database string, name string, clusterName *string, kind schemaObjectKind) (*schemaObject, error) {
	whereConditions := []querybuilder.Where{
		querybuilder.WhereEquals("database", database),
		querybuilder.WhereEquals("name", name),
	}
	switch kind {
	case schemaObjectKindTable:
		whereConditions = append(whereConditions,
			querybuilder.WhereDiffers("engine", "View"),
			querybuilder.WhereDiffers("engine", "MaterializedView"),
		)
	case schemaObjectKindView:
		whereConditions = append(whereConditions, querybuilder.WhereEquals("engine", "View"))
	case schemaObjectKindMaterializedView:
		whereConditions = append(whereConditions, querybuilder.WhereEquals("engine", "MaterializedView"))
	}

	sql, err := querybuilder.NewSelect(
		[]querybuilder.Field{
			querybuilder.NewField("database"),
			querybuilder.NewField("name"),
			querybuilder.NewField("engine"),
			querybuilder.NewField("engine_full"),
			querybuilder.NewField("create_table_query"),
		},
		"system.tables",
	).WithCluster(clusterName).Where(whereConditions...).Build()
	if err != nil {
		return nil, errors.WithMessage(err, "error building query")
	}

	var object *schemaObject

	err = i.clickhouseClient.Select(ctx, sql, func(data clickhouseclient.Row) error {
		if object != nil {
			return nil
		}

		dbName, err := data.GetString("database")
		if err != nil {
			return errors.WithMessage(err, "error scanning query result, missing 'database' field")
		}
		objectName, err := data.GetString("name")
		if err != nil {
			return errors.WithMessage(err, "error scanning query result, missing 'name' field")
		}
		engine, err := data.GetString("engine")
		if err != nil {
			return errors.WithMessage(err, "error scanning query result, missing 'engine' field")
		}
		engineFull, err := data.GetString("engine_full")
		if err != nil {
			return errors.WithMessage(err, "error scanning query result, missing 'engine_full' field")
		}
		createStatement, err := data.GetString("create_table_query")
		if err != nil {
			return errors.WithMessage(err, "error scanning query result, missing 'create_table_query' field")
		}

		object = &schemaObject{
			Database:        dbName,
			Name:            objectName,
			Engine:          engine,
			EngineFull:      engineFull,
			CreateStatement: createStatement,
		}

		return nil
	})
	if err != nil {
		return nil, errors.WithMessage(err, "error running query")
	}

	if object == nil {
		return nil, nil
	}

	if strings.TrimSpace(object.EngineFull) == "" {
		object.EngineFull = object.Engine
	}

	return object, nil
}

type createTableDefinition struct {
	Engine      string
	PartitionBy string
	OrderBy     string
	PrimaryKey  string
	SampleBy    string
	TTL         string
	Settings    string
	AsSelect    string
}

type createTableClause struct {
	keyword string
	index   int
}

type createViewDefinition struct {
	Columns []Column
	Query   string
}

type createMaterializedViewDefinition struct {
	Columns     []Column
	Engine      string
	PartitionBy string
	OrderBy     string
	PrimaryKey  string
	SampleBy    string
	TTL         string
	Settings    string
	Populate    bool
	ToTable     string
	ToColumns   []Column
	Query       string
}

func parseCreateTableDefinition(createStatement string) (createTableDefinition, error) {
	definition := createTableDefinition{}
	statement := strings.TrimSpace(strings.TrimSuffix(createStatement, ";"))
	if statement == "" {
		return definition, nil
	}

	engineStart, err := querybuilder.FindTopLevelKeyword(statement, "ENGINE =", 0)
	if err != nil {
		return definition, err
	}
	if engineStart == -1 {
		return definition, errors.New("unable to locate ENGINE clause in CREATE TABLE statement")
	}

	engineValueStart := engineStart + len("ENGINE =")
	nextClause, err := findNextCreateTableClause(statement, engineValueStart)
	if err != nil {
		return definition, err
	}
	engineValueEnd := len(statement)
	if nextClause != nil {
		engineValueEnd = nextClause.index
	}
	definition.Engine = strings.TrimSpace(statement[engineValueStart:engineValueEnd])

	for clause := nextClause; clause != nil; {
		valueStart := clause.index + len(clause.keyword)
		nextClause, err = findNextCreateTableClause(statement, valueStart)
		if err != nil {
			return definition, err
		}
		valueEnd := len(statement)
		if nextClause != nil {
			valueEnd = nextClause.index
		}

		value := strings.TrimSpace(statement[valueStart:valueEnd])
		switch clause.keyword {
		case "PARTITION BY":
			definition.PartitionBy = value
		case "ORDER BY":
			definition.OrderBy = value
		case "PRIMARY KEY":
			definition.PrimaryKey = value
		case "SAMPLE BY":
			definition.SampleBy = value
		case "TTL":
			definition.TTL = value
		case "SETTINGS":
			definition.Settings = value
		case "AS":
			definition.AsSelect = value
			return definition, nil
		}

		clause = nextClause
	}

	return definition, nil
}

func parseCreateViewDefinition(createStatement string) (createViewDefinition, error) {
	definition := createViewDefinition{}
	statement := strings.TrimSpace(strings.TrimSuffix(createStatement, ";"))
	if statement == "" {
		return definition, nil
	}

	asIndex, err := querybuilder.FindTopLevelKeyword(statement, "AS", 0)
	if err != nil {
		return definition, err
	}
	if asIndex == -1 {
		return definition, errors.New("unable to locate AS clause in CREATE VIEW statement")
	}

	definition.Query = strings.TrimSpace(statement[asIndex+len("AS"):])
	prefix := strings.TrimSpace(statement[:asIndex])
	if prefix == "" {
		return definition, nil
	}

	openIndex, closeIndex, ok, err := querybuilder.FindTrailingTopLevelParentheses(prefix)
	if err != nil {
		return definition, err
	}
	if !ok {
		return definition, nil
	}

	columns, err := parseColumnSignatures(prefix[openIndex+1 : closeIndex])
	if err != nil {
		return definition, err
	}
	definition.Columns = columns
	return definition, nil
}

func parseCreateMaterializedViewDefinition(createStatement string) (createMaterializedViewDefinition, error) {
	definition := createMaterializedViewDefinition{}
	statement := strings.TrimSpace(strings.TrimSuffix(createStatement, ";"))
	if statement == "" {
		return definition, nil
	}

	// Find AS keyword — the query always follows AS
	asIndex, err := querybuilder.FindTopLevelKeyword(statement, "AS", 0)
	if err != nil {
		return definition, err
	}
	if asIndex == -1 {
		return definition, errors.New("unable to locate AS clause in CREATE MATERIALIZED VIEW statement")
	}
	definition.Query = strings.TrimSpace(statement[asIndex+len("AS"):])

	prefix := strings.TrimSpace(statement[:asIndex])
	populateIndex, err := querybuilder.FindTopLevelKeyword(prefix, "POPULATE", 0)
	if err != nil {
		return definition, err
	}
	if populateIndex != -1 {
		definition.Populate = true
		prefix = strings.TrimSpace(strings.TrimSpace(prefix[:populateIndex]) + " " + strings.TrimSpace(prefix[populateIndex+len("POPULATE"):]))
	}

	// Check for TO <table> clause
	toIndex, err := querybuilder.FindTopLevelKeyword(prefix, "TO", 0)
	if err != nil {
		return definition, err
	}
	if toIndex != -1 {
		toValue := strings.TrimSpace(prefix[toIndex+len("TO"):])
		// TO table may be followed by column signatures in parens — strip those
		if openIdx, closeIdx, ok, err := querybuilder.FindTrailingTopLevelParentheses(toValue); err != nil {
			return definition, err
		} else if ok {
			columns, parseErr := parseColumnSignatures(toValue[openIdx+1 : closeIdx])
			if parseErr != nil {
				return definition, parseErr
			}
			definition.ToColumns = columns
			toValue = strings.TrimSpace(toValue[:openIdx])
		}
		definition.ToTable = toValue
		return definition, nil
	}

	// No TO clause — check for ENGINE = clause (engine-backed materialized view)
	engineIndex, err := querybuilder.FindTopLevelKeyword(prefix, "ENGINE =", 0)
	if err != nil {
		return definition, err
	}
	if engineIndex != -1 {
		engineValueStart := engineIndex + len("ENGINE =")
		nextClause, clauseErr := findNextCreateTableClause(prefix, engineValueStart)
		if clauseErr != nil {
			return definition, clauseErr
		}
		engineValueEnd := len(prefix)
		if nextClause != nil {
			engineValueEnd = nextClause.index
		}
		definition.Engine = strings.TrimSpace(prefix[engineValueStart:engineValueEnd])

		for clause := nextClause; clause != nil; {
			valueStart := clause.index + len(clause.keyword)
			nextClause, clauseErr = findNextCreateTableClause(prefix, valueStart)
			if clauseErr != nil {
				return definition, clauseErr
			}
			valueEnd := len(prefix)
			if nextClause != nil {
				valueEnd = nextClause.index
			}

			value := strings.TrimSpace(prefix[valueStart:valueEnd])
			switch clause.keyword {
			case "PARTITION BY":
				definition.PartitionBy = value
			case "ORDER BY":
				definition.OrderBy = value
			case "PRIMARY KEY":
				definition.PrimaryKey = value
			case "SAMPLE BY":
				definition.SampleBy = value
			case "TTL":
				definition.TTL = value
			case "SETTINGS":
				definition.Settings = value
			}

			clause = nextClause
		}

		// Engine value may be preceded by column definitions in parens.
		columnPrefix := strings.TrimSpace(prefix[:engineIndex])
		if openIdx, closeIdx, ok, parseErr := querybuilder.FindTrailingTopLevelParentheses(columnPrefix); parseErr != nil {
			return definition, parseErr
		} else if ok {
			elements, parseErr := parseTableElements(columnPrefix[openIdx+1 : closeIdx])
			if parseErr != nil {
				return definition, parseErr
			}
			definition.Columns = elements.Columns
		}
		return definition, nil
	}

	// No TO and no ENGINE — check for column signatures in prefix
	if openIdx, closeIdx, ok, parseErr := querybuilder.FindTrailingTopLevelParentheses(prefix); parseErr != nil {
		return definition, parseErr
	} else if ok {
		columns, parseErr := parseColumnSignatures(prefix[openIdx+1 : closeIdx])
		if parseErr != nil {
			return definition, parseErr
		}
		definition.Columns = columns
	}

	return definition, nil
}

func findNextCreateTableClause(raw string, start int) (*createTableClause, error) {
	keywords := []string{"PARTITION BY", "ORDER BY", "PRIMARY KEY", "SAMPLE BY", "TTL", "SETTINGS", "AS"}

	var next *createTableClause
	for _, keyword := range keywords {
		index, err := querybuilder.FindTopLevelKeyword(raw, keyword, start)
		if err != nil {
			return nil, err
		}
		if index == -1 {
			continue
		}
		if next == nil || index < next.index {
			next = &createTableClause{keyword: keyword, index: index}
		}
	}

	return next, nil
}

func parseColumnSignatures(raw string) ([]Column, error) {
	parts, err := querybuilder.SplitTopLevelCSV(raw)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, nil
	}

	columns := make([]Column, 0, len(parts))
	for _, part := range parts {
		column, err := parseColumnSignature(part)
		if err != nil {
			return nil, err
		}
		columns = append(columns, column)
	}

	return columns, nil
}

func parseColumnSignature(raw string) (Column, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Column{}, errors.New("empty column signature")
	}

	nameEnd, err := querybuilder.FindColumnNameEnd(raw)
	if err != nil {
		return Column{}, err
	}

	name := querybuilder.UnquoteIdentifier(strings.TrimSpace(raw[:nameEnd]))
	typeSQL := strings.TrimSpace(raw[nameEnd:])
	if name == "" || typeSQL == "" {
		return Column{}, errors.New("invalid column signature")
	}

	return Column{Name: name, Type: typeSQL}, nil
}

type tableElements struct {
	Columns     []Column
	Indexes     []Index
	Projections []Projection
	Constraints []Constraint
}

// parseCreateTableElements parses the first parenthesized list of a canonical CREATE TABLE
// statement: columns, indexes, projections and constraints.
func parseCreateTableElements(createStatement string) (tableElements, error) {
	state := querybuilder.SQLScanState{}
	for index := 0; index < len(createStatement); index++ {
		if state.IsTopLevel() && createStatement[index] == '(' {
			closeIndex, err := querybuilder.FindMatchingClose(createStatement, index)
			if err != nil {
				return tableElements{}, err
			}
			return parseTableElements(createStatement[index+1 : closeIndex])
		}
		var err error
		index, err = querybuilder.AdvanceSQLScanState(createStatement, index, &state)
		if err != nil {
			return tableElements{}, err
		}
	}

	return tableElements{}, nil
}

func parseTableElements(raw string) (tableElements, error) {
	elements := tableElements{}
	parts, err := querybuilder.SplitTopLevelCSV(raw)
	if err != nil {
		return elements, err
	}

	for _, part := range parts {
		switch {
		case strings.HasPrefix(part, "INDEX "):
			index, err := parseIndexDefinition(part[len("INDEX "):])
			if err != nil {
				return elements, errors.WithMessage(err, fmt.Sprintf("error parsing %q", part))
			}
			elements.Indexes = append(elements.Indexes, index)
		case strings.HasPrefix(part, "PROJECTION "):
			name, rest, err := splitElementName(part[len("PROJECTION "):])
			if err != nil {
				return elements, errors.WithMessage(err, fmt.Sprintf("error parsing %q", part))
			}
			projection := Projection{Name: name, Query: rest}
			if closeIndex, closeErr := querybuilder.FindMatchingClose(rest, 0); strings.HasPrefix(rest, "(") && closeErr == nil {
				projection.Query = strings.TrimSpace(rest[1:closeIndex])
				// PROJECTION name (query) WITH SETTINGS (a = 1)
				if tail := strings.TrimSpace(rest[closeIndex+1:]); strings.HasPrefix(tail, "WITH SETTINGS") {
					settings := strings.TrimSpace(strings.TrimPrefix(tail, "WITH SETTINGS"))
					projection.Settings = strings.TrimSuffix(strings.TrimPrefix(settings, "("), ")")
				}
			}
			elements.Projections = append(elements.Projections, projection)
		case strings.HasPrefix(part, "CONSTRAINT "):
			name, rest, err := splitElementName(part[len("CONSTRAINT "):])
			if err != nil {
				return elements, errors.WithMessage(err, fmt.Sprintf("error parsing %q", part))
			}
			if !strings.HasPrefix(rest, "CHECK ") {
				return elements, fmt.Errorf("unsupported constraint %q: only CHECK constraints are supported", part)
			}
			elements.Constraints = append(elements.Constraints, Constraint{Name: name, Check: strings.TrimSpace(rest[len("CHECK "):])})
		default:
			column, err := parseColumnDefinition(part)
			if err != nil {
				return elements, errors.WithMessage(err, fmt.Sprintf("error parsing %q", part))
			}
			elements.Columns = append(elements.Columns, column)
		}
	}

	return elements, nil
}

// splitElementName splits "name rest" into the unquoted name and the trimmed rest.
func splitElementName(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	nameEnd, err := querybuilder.FindColumnNameEnd(raw)
	if err != nil {
		return "", "", err
	}

	return querybuilder.UnquoteIdentifier(raw[:nameEnd]), strings.TrimSpace(raw[nameEnd:]), nil
}

// parseIndexDefinition parses "name expr TYPE type [GRANULARITY n]".
func parseIndexDefinition(raw string) (Index, error) {
	name, rest, err := splitElementName(raw)
	if err != nil {
		return Index{}, err
	}

	typeIndex, err := querybuilder.FindTopLevelKeyword(rest, "TYPE", 0)
	if err != nil {
		return Index{}, err
	}
	if typeIndex == -1 {
		return Index{}, errors.New("index definition has no TYPE clause")
	}

	index := Index{
		Name:        name,
		Expression:  strings.TrimSpace(rest[:typeIndex]),
		Type:        strings.TrimSpace(rest[typeIndex+len("TYPE"):]),
		Granularity: 1,
	}

	granularityIndex, err := querybuilder.FindTopLevelKeyword(index.Type, "GRANULARITY", 0)
	if err != nil {
		return Index{}, err
	}
	if granularityIndex != -1 {
		index.Granularity, err = strconv.ParseInt(strings.TrimSpace(index.Type[granularityIndex+len("GRANULARITY"):]), 10, 64)
		if err != nil {
			return Index{}, errors.WithMessage(err, "invalid index granularity")
		}
		index.Type = strings.TrimSpace(index.Type[:granularityIndex])
	}

	return index, nil
}

// parseColumnDefinition parses a canonical column definition:
// name Type [DEFAULT e | MATERIALIZED e | ALIAS e | EPHEMERAL e] [COMMENT 'c'] [CODEC(...)] [TTL e]
func parseColumnDefinition(raw string) (Column, error) {
	name, rest, err := splitElementName(raw)
	if err != nil {
		return Column{}, err
	}

	type clause struct {
		keyword string
		index   int
	}
	clauses := make([]clause, 0)
	for _, keyword := range []string{"DEFAULT", "MATERIALIZED", "ALIAS", "EPHEMERAL", "COMMENT", "CODEC", "TTL"} {
		index, err := querybuilder.FindTopLevelKeyword(rest, keyword, 0)
		if err != nil {
			return Column{}, err
		}
		if index != -1 {
			clauses = append(clauses, clause{keyword: keyword, index: index})
		}
	}
	sort.Slice(clauses, func(a, b int) bool { return clauses[a].index < clauses[b].index })

	typeEnd := len(rest)
	if len(clauses) > 0 {
		typeEnd = clauses[0].index
	}
	column := Column{Name: name, Type: strings.TrimSpace(rest[:typeEnd])}
	if column.Name == "" || column.Type == "" {
		return Column{}, errors.New("invalid column definition")
	}

	for position, current := range clauses {
		end := len(rest)
		if position+1 < len(clauses) {
			end = clauses[position+1].index
		}
		value := strings.TrimSpace(rest[current.index+len(current.keyword) : end])

		switch current.keyword {
		case "DEFAULT":
			column.DefaultExpression = &value
		case "MATERIALIZED":
			column.MaterializedExpression = &value
		case "ALIAS":
			column.AliasExpression = &value
		case "EPHEMERAL":
			// EPHEMERAL without an expression stays in the type text, because the
			// ephemeral_expression attribute cannot be empty.
			if value == "" {
				column.Type += " EPHEMERAL"
				continue
			}
			column.EphemeralExpression = &value
		case "COMMENT":
			column.Comment = unquoteString(value)
		case "CODEC":
			column.Codec = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "("), ")"))
		case "TTL":
			column.TTL = value
		}
	}

	return column, nil
}

// unquoteString returns the value of a single-quoted SQL string literal.
func unquoteString(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '\'' || raw[len(raw)-1] != '\'' {
		return raw
	}

	var out strings.Builder
	inner := raw[1 : len(raw)-1]
	for index := 0; index < len(inner); index++ {
		ch := inner[index]
		if (ch == '\\' || ch == '\'') && index+1 < len(inner) {
			index++
			switch next := inner[index]; {
			case ch == '\\' && next == 'n':
				out.WriteByte('\n')
			case ch == '\\' && next == 't':
				out.WriteByte('\t')
			default:
				out.WriteByte(next)
			}
			continue
		}
		out.WriteByte(ch)
	}

	return out.String()
}

// ToQueryBuilderColumn converts a single Column to a querybuilder.ColumnDefinition.
func ToQueryBuilderColumn(column Column) querybuilder.ColumnDefinition {
	var comment *string
	if column.Comment != "" {
		comment = &column.Comment
	}

	return querybuilder.ColumnDefinition{
		Name:                   column.Name,
		Type:                   column.Type,
		Nullable:               column.Nullable,
		Comment:                comment,
		DefaultExpression:      column.DefaultExpression,
		MaterializedExpression: column.MaterializedExpression,
		AliasExpression:        column.AliasExpression,
		EphemeralExpression:    column.EphemeralExpression,
		Codec:                  column.Codec,
		TTL:                    column.TTL,
	}
}

func toQueryBuilderColumns(columns []Column) []querybuilder.ColumnDefinition {
	if len(columns) == 0 {
		return nil
	}

	ret := make([]querybuilder.ColumnDefinition, 0, len(columns))
	for _, column := range columns {
		ret = append(ret, ToQueryBuilderColumn(column))
	}

	return ret
}
