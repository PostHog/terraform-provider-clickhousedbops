package table

import (
	"slices"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
)

// A new replica uses the reference replica's physical schema, including unmanaged
// columns. Dry-run ALTERs must preserve that order rather than append unmanaged fields.
func previewUpdatedTable(existing, current, desired dbops.Table) (dbops.Table, error) {
	renamed, _, err := applySafeRenames(current, desired)
	if err != nil {
		return dbops.Table{}, err
	}
	renames := map[string]string{}
	managedOrder := make([]string, 0, len(renamed))
	for index, column := range renamed {
		renames[current.Columns[index].Name] = column.Name
		managedOrder = append(managedOrder, column.Name)
	}
	physicalOrder := make([]string, 0, len(existing.Columns))
	columns := map[string]dbops.Column{}
	for _, column := range existing.Columns {
		if name, ok := renames[column.Name]; ok {
			column.Name = name
		}
		physicalOrder = append(physicalOrder, column.Name)
		columns[column.Name] = column
	}
	var moves [][2]string
	for index, column := range desired.Columns {
		previous := ""
		if index > 0 {
			previous = desired.Columns[index-1].Name
		}
		if !slices.Contains(managedOrder, column.Name) {
			physicalOrder = insertNameAfter(physicalOrder, column.Name, previous)
			managedOrder = insertNameAfter(managedOrder, column.Name, previous)
		} else if !columnInDesiredPosition(managedOrder, column.Name, previous) {
			moves = append(moves, [2]string{column.Name, previous})
			managedOrder = moveNameAfter(managedOrder, column.Name, previous)
		}
		columns[column.Name] = column
	}
	// The planner executes the ADD group before the MODIFY group.
	for _, move := range moves {
		physicalOrder = moveNameAfter(physicalOrder, move[0], move[1])
	}
	preview := desired
	preview.Columns = nil
	for _, name := range physicalOrder {
		wasManaged := slices.Contains(managedOrder, name)
		stillDeclared := slices.ContainsFunc(desired.Columns, func(c dbops.Column) bool { return c.Name == name })
		if !wasManaged || stillDeclared {
			preview.Columns = append(preview.Columns, columns[name])
		}
	}
	preview.Indexes = previewNamedSchema(existing.Indexes, current.Indexes, desired.Indexes, indexName, indexEqual)
	preview.Projections = previewNamedSchema(existing.Projections, current.Projections, desired.Projections, projectionName, projectionEqual)
	preview.Constraints = previewNamedSchema(existing.Constraints, current.Constraints, desired.Constraints, constraintName, constraintEqual)
	return preview, nil
}

func previewNamedSchema[T any](existing, managed, desired []T, name func(T) string, equal func(T, T) bool) []T {
	result := make([]T, 0, len(existing)+len(desired))
	for _, item := range existing {
		wasManaged := slices.ContainsFunc(managed, func(m T) bool { return name(m) == name(item) })
		unchanged := slices.ContainsFunc(desired, func(d T) bool { return name(d) == name(item) && equal(d, item) })
		if !wasManaged || unchanged {
			result = append(result, item)
		}
	}
	for _, item := range desired {
		if !slices.ContainsFunc(result, func(r T) bool { return name(r) == name(item) }) {
			result = append(result, item)
		}
	}
	return result
}
