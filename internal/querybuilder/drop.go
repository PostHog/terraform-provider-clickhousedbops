package querybuilder

import (
	"strings"

	"github.com/pingcap/errors"
)

const (
	resourceTypeDatabase        = "DATABASE"
	resourceTypeDictionary      = "DICTIONARY"
	resourceTypeTable           = "TABLE"
	resourceTypeRole            = "ROLE"
	resourceTypeUser            = "USER"
	resourceTypeView            = "VIEW"
	resourceTypeSettingsProfile = "SETTINGS PROFILE"
	resourceTypeNamedCollection = "NAMED COLLECTION"
)

type DropQueryBuilder interface {
	QueryBuilder
	WithCluster(clusterName *string) DropQueryBuilder
	IfExists(ifExists bool) DropQueryBuilder
	// SkipDependencyCheck drops the object even while a dictionary or view reads from it.
	SkipDependencyCheck(skip bool) DropQueryBuilder
}

type dropQueryBuilder struct {
	resourceTypeName string
	resourceName     string
	resourceNameSQL  string
	clusterName      *string
	ifExists         bool
	sync             bool
	skipDependencies bool
}

func NewDropRole(resourceName string) DropQueryBuilder {
	return newDrop(resourceTypeRole, resourceName)
}

func NewDropDatabase(resourceName string) DropQueryBuilder {
	return newDrop(resourceTypeDatabase, resourceName)
}

func NewDropDictionary(database string, name string) DropQueryBuilder {
	return newDropQualified(resourceTypeDictionary, database, name)
}

func NewDropTable(database string, name string) DropQueryBuilder {
	return newDropQualified(resourceTypeTable, database, name)
}

func NewDropUser(resourceName string) DropQueryBuilder {
	return newDrop(resourceTypeUser, resourceName)
}

func NewDropView(database string, name string) DropQueryBuilder {
	return newDropQualified(resourceTypeView, database, name)
}

func NewDropMaterializedView(database string, name string) DropQueryBuilder {
	return newDropQualified(resourceTypeView, database, name)
}

func NewDropSettingsProfile(resourceName string) DropQueryBuilder {
	return newDrop(resourceTypeSettingsProfile, resourceName)
}

func NewDropNamedCollection(resourceName string) DropQueryBuilder {
	return newDrop(resourceTypeNamedCollection, resourceName)
}

func (q *dropQueryBuilder) WithCluster(clusterName *string) DropQueryBuilder {
	q.clusterName = clusterName
	return q
}

func (q *dropQueryBuilder) IfExists(ifExists bool) DropQueryBuilder {
	q.ifExists = ifExists
	return q
}

func (q *dropQueryBuilder) SkipDependencyCheck(skip bool) DropQueryBuilder {
	q.skipDependencies = skip
	return q
}

func newDrop(resourceTypeName string, resourceName string) DropQueryBuilder {
	return &dropQueryBuilder{
		resourceTypeName: resourceTypeName,
		resourceName:     strings.TrimSpace(resourceName),
	}
}

func newDropQualified(resourceTypeName string, database string, name string) DropQueryBuilder {
	database = strings.TrimSpace(database)
	name = strings.TrimSpace(name)

	resourceNameSQL := ""
	if database != "" && name != "" {
		resourceNameSQL = qualifiedIdentifier(database, name)
	}

	// Schema objects are dropped with IF EXISTS ... SYNC: a replicated table that is dropped
	// and created again in one apply must release its replica path in Keeper first.
	return &dropQueryBuilder{
		resourceTypeName: resourceTypeName,
		resourceNameSQL:  resourceNameSQL,
		ifExists:         true,
		sync:             true,
	}
}

func (q *dropQueryBuilder) Build() (string, error) {
	if q.resourceName == "" && q.resourceNameSQL == "" {
		return "", errors.New("resource name cannot be empty for DROP queries")
	}

	resourceNameSQL := q.resourceNameSQL
	if resourceNameSQL == "" {
		resourceNameSQL = backtick(q.resourceName)
	}

	tokens := []string{
		"DROP",
		q.resourceTypeName,
	}

	if q.ifExists {
		tokens = append(tokens, "IF", "EXISTS")
	}

	tokens = append(tokens, resourceNameSQL)
	tokens = appendClusterClause(tokens, q.clusterName)
	if q.sync {
		tokens = append(tokens, "SYNC")
	}
	if q.skipDependencies {
		tokens = append(tokens, "SETTINGS", "check_table_dependencies", "=", "0")
	}

	return strings.Join(tokens, " ") + ";", nil
}
