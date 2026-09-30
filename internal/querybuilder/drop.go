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
}

type dropQueryBuilder struct {
	resourceTypeName string
	resourceName     string
	resourceNameSQL  string
	clusterName      *string
	ifExists         bool
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

	return &dropQueryBuilder{
		resourceTypeName: resourceTypeName,
		resourceNameSQL:  resourceNameSQL,
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

	return strings.Join(tokens, " ") + ";", nil
}
