package table

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/querybuilder"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/tableengine"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/schemahelpers"
)

type plannedTableUpdate struct {
	ReplaceAttrs map[string]struct{}
	ActionGroups [][]string
	// SettingGroups holds the groups of ActionGroups that change table settings. On a
	// Replicated table they apply to one replica only; every other ALTER reaches all replicas
	// through Keeper.
	SettingGroups [][]string
}

type engineUpdateStrategy struct {
	family             tableengine.Family
	allowColumnAlter   bool
	allowSettingsAlter bool
	allowSampleByAlter bool
	allowTTLAlter      bool
	allowOrderByAlter  bool
	allowIndexAlter    bool
}

type columnUpdatePlan struct {
	replaceRequired bool
	renameActions   []string
	addActions      []string
	modifyActions   []string
	dropActions     []string
	addedColumns    map[string]dbops.Column
}

type parsedSettings struct {
	ordered []settingAssignment
	values  map[string]string
}

type settingAssignment struct {
	Name  string
	Value string
}

type columnExpression struct {
	kind querybuilder.ColumnExpressionKind
	sql  string
}

func validateTableForEngine(table dbops.Table, capabilities dbops.TableEngineCapabilities) diag.Diagnostics {
	var diags diag.Diagnostics

	family := tableengine.FamilyForName(capabilities.Name)

	if capabilities.Known {
		if family == tableengine.FamilyMergeTree && normalizeSQL(table.OrderBy) == "" {
			diags.AddError(
				"Missing ORDER BY clause",
				fmt.Sprintf("The %q engine family requires ORDER BY. Use at least tuple() when no sort key is needed.", capabilities.Name),
			)
		}
		if !capabilities.SupportsSettings && normalizeSQL(table.Settings) != "" {
			diags.AddError(
				"Unsupported table settings",
				fmt.Sprintf("The %q engine does not support a table-level SETTINGS clause.", capabilities.Name),
			)
		}
		if !capabilities.SupportsSortOrder {
			if normalizeSQL(table.PartitionBy) != "" {
				diags.AddError("Unsupported PARTITION BY clause", fmt.Sprintf("The %q engine does not support PARTITION BY.", capabilities.Name))
			}
			if normalizeSQL(table.OrderBy) != "" {
				diags.AddError("Unsupported ORDER BY clause", fmt.Sprintf("The %q engine does not support ORDER BY.", capabilities.Name))
			}
			if normalizeSQL(table.PrimaryKey) != "" {
				diags.AddError("Unsupported PRIMARY KEY clause", fmt.Sprintf("The %q engine does not support PRIMARY KEY.", capabilities.Name))
			}
			if normalizeSQL(table.SampleBy) != "" {
				diags.AddError("Unsupported SAMPLE BY clause", fmt.Sprintf("The %q engine does not support SAMPLE BY.", capabilities.Name))
			}
		}
		if !capabilities.SupportsTTL && normalizeSQL(table.TTL) != "" {
			diags.AddError("Unsupported TTL clause", fmt.Sprintf("The %q engine does not support TTL.", capabilities.Name))
		}
	}

	if family == tableengine.FamilyKafka {
		insertableColumns := 0
		for _, column := range table.Columns {
			if column.DefaultExpression != nil {
				diags.AddError(
					"Unsupported Kafka column default",
					fmt.Sprintf("Kafka tables do not support DEFAULT column expressions. Column %q defines one.", column.Name),
				)
			}
			if column.MaterializedExpression != nil {
				diags.AddError(
					"Unsupported Kafka materialized column",
					fmt.Sprintf("Kafka tables do not support MATERIALIZED column expressions. Column %q defines one.", column.Name),
				)
			}
			if column.AliasExpression == nil {
				insertableColumns++
			}
		}
		if len(table.Columns) > 0 && insertableColumns == 0 {
			diags.AddError(
				"Invalid Kafka column layout",
				"Kafka tables must expose at least one insertable column. A schema made entirely of ALIAS columns cannot be created.",
			)
		}
	}

	return diags
}

func planTableUpdate(current dbops.Table, desired dbops.Table, capabilities dbops.TableEngineCapabilities, settingCapabilities map[string]dbops.TableSettingCapability) (plannedTableUpdate, error) {
	plan := plannedTableUpdate{
		ReplaceAttrs: make(map[string]struct{}),
	}

	if !expressionsEqual(current.PartitionBy, desired.PartitionBy) {
		plan.ReplaceAttrs["partition_by"] = struct{}{}
	}
	if !expressionListsEqual(current.PrimaryKey, desired.PrimaryKey) {
		plan.ReplaceAttrs["primary_key"] = struct{}{}
	}

	strategy := buildEngineUpdateStrategy(capabilities)

	columnPlan, err := planColumnUpdate(current, desired, strategy)
	if err != nil {
		return plannedTableUpdate{}, err
	}
	if columnPlan.replaceRequired {
		plan.ReplaceAttrs["columns"] = struct{}{}
	}

	if orderChanged := !expressionListsEqual(current.OrderBy, desired.OrderBy); orderChanged {
		if !strategy.allowOrderByAlter || columnPlan.replaceRequired {
			plan.ReplaceAttrs["order_by"] = struct{}{}
		} else {
			action, ok, actionErr := buildOrderByAlterAction(current.OrderBy, desired.OrderBy, columnPlan.addedColumns)
			if actionErr != nil {
				return plannedTableUpdate{}, actionErr
			}
			if !ok {
				plan.ReplaceAttrs["order_by"] = struct{}{}
			} else {
				columnPlan.addActions = append(columnPlan.addActions, action)
			}
		}
	}

	indexDrops, indexAdds := planNamedChanges(current.Indexes, desired.Indexes, indexName, indexEqual, querybuilder.BuildAddIndexAction, querybuilder.BuildDropIndexAction)
	projectionDrops, projectionAdds := planNamedChanges(current.Projections, desired.Projections, projectionName, projectionEqual, querybuilder.BuildAddProjectionAction, querybuilder.BuildDropProjectionAction)
	constraintDrops, constraintAdds := planNamedChanges(current.Constraints, desired.Constraints, constraintName, constraintEqual, querybuilder.BuildAddConstraintAction, querybuilder.BuildDropConstraintAction)
	if !strategy.allowIndexAlter {
		if len(indexDrops)+len(indexAdds) > 0 {
			plan.ReplaceAttrs["indexes"] = struct{}{}
		}
		if len(projectionDrops)+len(projectionAdds) > 0 {
			plan.ReplaceAttrs["projections"] = struct{}{}
		}
		if len(constraintDrops)+len(constraintAdds) > 0 {
			plan.ReplaceAttrs["constraints"] = struct{}{}
		}
	}
	metadataDrops := slices.Concat(indexDrops, projectionDrops, constraintDrops)
	metadataAdds := slices.Concat(indexAdds, projectionAdds, constraintAdds)

	tableActions := make([]string, 0)

	if !expressionsEqual(current.SampleBy, desired.SampleBy) {
		if !strategy.allowSampleByAlter {
			plan.ReplaceAttrs["sample_by"] = struct{}{}
		} else if normalizeSQL(desired.SampleBy) == "" {
			tableActions = append(tableActions, querybuilder.BuildRemoveSampleByAction())
		} else {
			action, actionErr := querybuilder.BuildModifySampleByAction(desired.SampleBy)
			if actionErr != nil {
				return plannedTableUpdate{}, actionErr
			}
			tableActions = append(tableActions, action)
		}
	}

	if !ttlExpressionsEqual(current.TTL, desired.TTL) {
		if !strategy.allowTTLAlter {
			plan.ReplaceAttrs["ttl"] = struct{}{}
		} else if normalizeSQL(desired.TTL) == "" {
			tableActions = append(tableActions, querybuilder.BuildRemoveTTLAction())
		} else {
			action, actionErr := querybuilder.BuildModifyTTLAction(desired.TTL)
			if actionErr != nil {
				return plannedTableUpdate{}, actionErr
			}
			tableActions = append(tableActions, action)
		}
	}

	settingsActions, settingsReplace, err := planSettingsUpdate(current.Settings, desired.Settings, strategy, settingCapabilities)
	if err != nil {
		return plannedTableUpdate{}, err
	}
	if settingsReplace {
		plan.ReplaceAttrs["settings"] = struct{}{}
	}

	if len(plan.ReplaceAttrs) > 0 {
		return plan, nil
	}

	if len(columnPlan.renameActions) > 0 {
		plan.ActionGroups = append(plan.ActionGroups, columnPlan.renameActions)
	}
	// Indexes, projections and constraints are dropped before the columns change and added
	// after it, because their expressions can name added or dropped columns.
	if len(metadataDrops) > 0 {
		plan.ActionGroups = append(plan.ActionGroups, metadataDrops)
	}
	if len(columnPlan.addActions) > 0 {
		plan.ActionGroups = append(plan.ActionGroups, columnPlan.addActions)
	}
	if len(columnPlan.modifyActions) > 0 {
		plan.ActionGroups = append(plan.ActionGroups, columnPlan.modifyActions)
	}
	if len(metadataAdds) > 0 {
		plan.ActionGroups = append(plan.ActionGroups, metadataAdds)
	}
	if len(tableActions) > 0 {
		plan.ActionGroups = append(plan.ActionGroups, tableActions)
	}
	plan.ActionGroups = append(plan.ActionGroups, settingsActions...)
	plan.SettingGroups = settingsActions
	if len(columnPlan.dropActions) > 0 {
		plan.ActionGroups = append(plan.ActionGroups, columnPlan.dropActions)
	}

	return plan, nil
}

func buildEngineUpdateStrategy(capabilities dbops.TableEngineCapabilities) engineUpdateStrategy {
	family := tableengine.FamilyForName(capabilities.Name)
	switch family {
	case tableengine.FamilyMergeTree:
		return engineUpdateStrategy{
			family:             family,
			allowColumnAlter:   true,
			allowSettingsAlter: true,
			allowSampleByAlter: true,
			allowTTLAlter:      true,
			allowOrderByAlter:  true,
			allowIndexAlter:    true,
		}
	case tableengine.FamilyDistributed:
		return engineUpdateStrategy{
			family:           family,
			allowColumnAlter: true,
		}
	default:
		return engineUpdateStrategy{family: family}
	}
}

func planColumnUpdate(current dbops.Table, desired dbops.Table, strategy engineUpdateStrategy) (columnUpdatePlan, error) {
	result := columnUpdatePlan{
		addedColumns: make(map[string]dbops.Column),
	}

	if schemahelpers.ColumnsEqual(current.Columns, desired.Columns) {
		return result, nil
	}
	if !strategy.allowColumnAlter {
		result.replaceRequired = true
		return result, nil
	}

	currentColumns, renameActions, renameErr := applySafeRenames(current, desired)
	if renameErr != nil {
		return columnUpdatePlan{}, renameErr
	}
	result.renameActions = renameActions

	if hasDuplicateColumnNames(currentColumns) || hasDuplicateColumnNames(desired.Columns) {
		result.replaceRequired = true
		return result, nil
	}

	currentMap := make(map[string]dbops.Column, len(currentColumns))
	currentOrder := make([]string, 0, len(currentColumns))
	for _, column := range currentColumns {
		currentMap[column.Name] = column
		currentOrder = append(currentOrder, column.Name)
	}

	desiredNames := make(map[string]struct{}, len(desired.Columns))
	for _, column := range desired.Columns {
		desiredNames[column.Name] = struct{}{}
	}

	refs := collectIdentifierReferences(current, desired)
	for _, column := range currentColumns {
		if _, ok := desiredNames[column.Name]; ok {
			continue
		}
		if refs[column.Name] {
			result.replaceRequired = true
			return result, nil
		}
	}

	for index, desiredColumn := range desired.Columns {
		previousName := ""
		if index > 0 {
			previousName = desired.Columns[index-1].Name
		}

		currentColumn, exists := currentMap[desiredColumn.Name]
		if !exists {
			position := columnPositionFor(previousName)
			action, err := querybuilder.BuildAddColumnAction(dbops.ToQueryBuilderColumn(desiredColumn), position)
			if err != nil {
				return columnUpdatePlan{}, err
			}
			result.addActions = append(result.addActions, action)
			result.addedColumns[desiredColumn.Name] = desiredColumn
			currentOrder = insertNameAfter(currentOrder, desiredColumn.Name, previousName)
			currentMap[desiredColumn.Name] = desiredColumn
			continue
		}

		positionChanged := !columnInDesiredPosition(currentOrder, desiredColumn.Name, previousName)
		commentChanged := normalizeSQL(currentColumn.Comment) != normalizeSQL(desiredColumn.Comment)
		typeChanged := !schemahelpers.TypesEqual(currentColumn.Type, currentColumn.Nullable, desiredColumn.Type, desiredColumn.Nullable)
		codecChanged := !schemahelpers.SQLEqual(currentColumn.Codec, desiredColumn.Codec)
		ttlChanged := !schemahelpers.SQLEqual(currentColumn.TTL, desiredColumn.TTL)
		oldExpression := extractColumnExpression(currentColumn)
		newExpression := extractColumnExpression(desiredColumn)
		expressionChanged := oldExpression.kind != newExpression.kind || normalizeSQL(oldExpression.sql) != normalizeSQL(newExpression.sql)

		if oldExpression.kind == querybuilder.ColumnExpressionKindEphemeral && oldExpression.kind != newExpression.kind {
			// ClickHouse has no REMOVE EPHEMERAL. MODIFY COLUMN with another kind of
			// expression replaces EPHEMERAL, but nothing makes the column ordinary again.
			if newExpression.kind == "" {
				result.replaceRequired = true
				return result, nil
			}
		} else if oldExpression.kind != "" && (oldExpression.kind != newExpression.kind || normalizeSQL(newExpression.sql) == "") {
			action, err := querybuilder.BuildRemoveColumnExpressionAction(desiredColumn.Name, oldExpression.kind)
			if err != nil {
				return columnUpdatePlan{}, err
			}
			result.modifyActions = append(result.modifyActions, action)
		}

		if codecChanged && normalizeSQL(desiredColumn.Codec) == "" {
			result.modifyActions = append(result.modifyActions, querybuilder.BuildRemoveColumnPropertyAction(desiredColumn.Name, "CODEC"))
		}
		if ttlChanged && normalizeSQL(desiredColumn.TTL) == "" {
			result.modifyActions = append(result.modifyActions, querybuilder.BuildRemoveColumnPropertyAction(desiredColumn.Name, "TTL"))
		}

		if typeChanged || positionChanged || (normalizeSQL(newExpression.sql) != "" && expressionChanged) ||
			(codecChanged && normalizeSQL(desiredColumn.Codec) != "") || (ttlChanged && normalizeSQL(desiredColumn.TTL) != "") {
			position := (*querybuilder.ColumnPosition)(nil)
			if positionChanged {
				position = columnPositionFor(previousName)
			}
			action, err := querybuilder.BuildModifyColumnAction(dbops.ToQueryBuilderColumn(desiredColumn), position)
			if err != nil {
				return columnUpdatePlan{}, err
			}
			result.modifyActions = append(result.modifyActions, action)
		}

		if commentChanged {
			var (
				action string
				err    error
			)
			if normalizeSQL(desiredColumn.Comment) == "" {
				action, err = querybuilder.BuildRemoveColumnCommentAction(desiredColumn.Name)
			} else {
				action, err = querybuilder.BuildCommentColumnAction(desiredColumn.Name, desiredColumn.Comment)
			}
			if err != nil {
				return columnUpdatePlan{}, err
			}
			result.modifyActions = append(result.modifyActions, action)
		}

		if positionChanged {
			currentOrder = moveNameAfter(currentOrder, desiredColumn.Name, previousName)
		}
		currentMap[desiredColumn.Name] = desiredColumn
	}

	for _, column := range currentColumns {
		if _, ok := desiredNames[column.Name]; ok {
			continue
		}
		action, err := querybuilder.BuildDropColumnAction(column.Name)
		if err != nil {
			return columnUpdatePlan{}, err
		}
		result.dropActions = append(result.dropActions, action)
	}

	return result, nil
}

func applySafeRenames(current dbops.Table, desired dbops.Table) ([]dbops.Column, []string, error) {
	renamed := make([]dbops.Column, len(current.Columns))
	copy(renamed, current.Columns)

	currentNames := make(map[string]struct{}, len(current.Columns))
	desiredNames := make(map[string]struct{}, len(desired.Columns))
	for _, column := range current.Columns {
		currentNames[column.Name] = struct{}{}
	}
	for _, column := range desired.Columns {
		desiredNames[column.Name] = struct{}{}
	}

	refs := collectIdentifierReferences(current, desired)
	actions := make([]string, 0)

	for idx := 0; idx < len(renamed) && idx < len(desired.Columns); idx++ {
		currentColumn := renamed[idx]
		desiredColumn := desired.Columns[idx]
		if currentColumn.Name == desiredColumn.Name {
			continue
		}
		if _, exists := desiredNames[currentColumn.Name]; exists {
			continue
		}
		if _, exists := currentNames[desiredColumn.Name]; exists {
			continue
		}
		if !columnsEqualIgnoringName(currentColumn, desiredColumn) {
			continue
		}
		if refs[currentColumn.Name] || refs[desiredColumn.Name] {
			continue
		}

		action, err := querybuilder.BuildRenameColumnAction(currentColumn.Name, desiredColumn.Name)
		if err != nil {
			return nil, nil, err
		}
		actions = append(actions, action)
		renamed[idx].Name = desiredColumn.Name
		delete(currentNames, currentColumn.Name)
		currentNames[desiredColumn.Name] = struct{}{}
	}

	return renamed, actions, nil
}

func buildOrderByAlterAction(current string, desired string, addedColumns map[string]dbops.Column) (string, bool, error) {
	currentExprs, err := splitExpressionList(current)
	if err != nil {
		return "", false, err
	}
	desiredExprs, err := splitExpressionList(desired)
	if err != nil {
		return "", false, err
	}
	if len(desiredExprs) <= len(currentExprs) || len(currentExprs) == 0 {
		return "", false, nil
	}
	for idx, currentExpr := range currentExprs {
		if normalizeSQL(currentExpr) != normalizeSQL(desiredExprs[idx]) {
			return "", false, nil
		}
	}

	for _, expr := range desiredExprs[len(currentExprs):] {
		columnName, ok := simpleIdentifier(expr)
		if !ok {
			return "", false, nil
		}
		column, ok := addedColumns[columnName]
		if !ok {
			return "", false, nil
		}
		if column.DefaultExpression != nil || column.MaterializedExpression != nil || column.AliasExpression != nil {
			return "", false, nil
		}
	}

	action, err := querybuilder.BuildModifyOrderByAction(desired)
	if err != nil {
		return "", false, err
	}

	return action, true, nil
}

func planSettingsUpdate(current string, desired string, strategy engineUpdateStrategy, capabilities map[string]dbops.TableSettingCapability) ([][]string, bool, error) {
	current = normalizeSQL(current)
	desired = normalizeSQL(desired)
	if current == desired {
		return nil, false, nil
	}

	currentParsed, err := parseSettings(current)
	if err != nil {
		return nil, false, fmt.Errorf("unable to parse current table settings: %w", err)
	}
	desiredParsed, err := parseSettings(desired)
	if err != nil {
		return nil, false, fmt.Errorf("unable to parse desired table settings: %w", err)
	}
	if settingsEqual(currentParsed, desiredParsed) {
		return nil, false, nil
	}
	if !strategy.allowSettingsAlter {
		return nil, true, nil
	}

	var modifyActions, resetActions []string
	seen := make(map[string]struct{})

	for _, setting := range desiredParsed.ordered {
		seen[setting.Name] = struct{}{}
		if currentValue, ok := currentParsed.values[setting.Name]; ok && normalizeSQL(currentValue) == normalizeSQL(setting.Value) {
			continue
		}
		capability, ok := capabilities[setting.Name]
		if !ok || !capability.Known || capability.Readonly {
			return nil, true, nil
		}
		action, err := querybuilder.BuildModifySettingAction(setting.Name, setting.Value)
		if err != nil {
			return nil, false, err
		}
		modifyActions = append(modifyActions, action)
	}

	for _, setting := range currentParsed.ordered {
		if _, ok := seen[setting.Name]; ok {
			continue
		}
		capability, ok := capabilities[setting.Name]
		// ClickHouse reports some read-only settings on every table, for example
		// index_granularity. One that holds the server default is not a change.
		if ok && capability.Known && capability.Readonly && strings.Trim(setting.Value, "'") == capability.Default {
			continue
		}
		if !ok || !capability.Known || capability.Readonly {
			return nil, true, nil
		}
		action, err := querybuilder.BuildResetSettingAction(setting.Name)
		if err != nil {
			return nil, false, err
		}
		resetActions = append(resetActions, action)
	}

	// Each group is one ALTER statement. ClickHouse reads everything after MODIFY SETTING or
	// RESET SETTING as one list of settings, so each kind is a single action in its own statement.
	var groups [][]string
	if len(modifyActions) > 0 {
		groups = append(groups, []string{mergeSettingActions("MODIFY SETTING ", modifyActions)})
	}
	if len(resetActions) > 0 {
		groups = append(groups, []string{mergeSettingActions("RESET SETTING ", resetActions)})
	}

	return groups, false, nil
}

func mergeSettingActions(prefix string, actions []string) string {
	merged := actions[0]
	for _, action := range actions[1:] {
		merged += ", " + strings.TrimPrefix(action, prefix)
	}
	return merged
}

func collectSettingNames(values ...string) []string {
	unique := make(map[string]struct{})
	for _, raw := range values {
		parsed, err := parseSettings(raw)
		if err != nil {
			continue
		}
		for name := range parsed.values {
			unique[name] = struct{}{}
		}
	}

	result := make([]string, 0, len(unique))
	for name := range unique {
		result = append(result, name)
	}
	return result
}

func parseSettings(raw string) (parsedSettings, error) {
	settings := parsedSettings{
		values: make(map[string]string),
	}
	raw = normalizeSQL(raw)
	if raw == "" {
		return settings, nil
	}

	parts, err := querybuilder.SplitTopLevel(raw, ',')
	if err != nil {
		return parsedSettings{}, err
	}

	for _, part := range parts {
		part = normalizeSQL(part)
		if part == "" {
			continue
		}
		name, value, found, splitErr := splitTopLevelPair(part, '=')
		if splitErr != nil {
			return parsedSettings{}, splitErr
		}
		if !found || normalizeSQL(name) == "" || normalizeSQL(value) == "" {
			return parsedSettings{}, fmt.Errorf("unable to parse table setting %q", part)
		}
		assignment := settingAssignment{
			Name:  querybuilder.UnquoteIdentifier(name),
			Value: normalizeSQL(value),
		}
		settings.ordered = append(settings.ordered, assignment)
		settings.values[assignment.Name] = assignment.Value
	}

	return settings, nil
}

func settingsEqual(left parsedSettings, right parsedSettings) bool {
	if len(left.values) != len(right.values) {
		return false
	}
	for name, value := range left.values {
		if normalizeSQL(right.values[name]) != normalizeSQL(value) {
			return false
		}
	}
	return true
}

func expressionsEqual(left string, right string) bool {
	return normalizeSQL(left) == normalizeSQL(right)
}

func expressionListsEqual(left string, right string) bool {
	leftParts, leftErr := splitExpressionList(left)
	rightParts, rightErr := splitExpressionList(right)
	if leftErr != nil || rightErr != nil {
		return normalizeSQL(left) == normalizeSQL(right)
	}
	if len(leftParts) != len(rightParts) {
		return false
	}
	for index := range leftParts {
		if normalizeSQL(leftParts[index]) != normalizeSQL(rightParts[index]) {
			return false
		}
	}
	return true
}

func ttlExpressionsEqual(left string, right string) bool {
	return normalizeTTLExpression(left) == normalizeTTLExpression(right)
}

func normalizeTTLExpression(value string) string {
	value = normalizeSQL(value)
	if value == "" {
		return ""
	}

	replacer := ttlIntervalFuncPattern.ReplaceAllStringFunc(value, func(match string) string {
		parts := ttlIntervalFuncPattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		return fmt.Sprintf("INTERVAL(%s,%s)", normalizeSQL(parts[2]), strings.ToUpper(parts[1]))
	})

	return ttlIntervalKeywordPattern.ReplaceAllStringFunc(replacer, func(match string) string {
		parts := ttlIntervalKeywordPattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		return fmt.Sprintf("INTERVAL(%s,%s)", normalizeSQL(parts[1]), strings.ToUpper(parts[2]))
	})
}

func splitExpressionList(raw string) ([]string, error) {
	raw = normalizeSQL(raw)
	if raw == "" {
		return nil, nil
	}
	raw = unwrapOuterParens(raw)
	parts, err := querybuilder.SplitTopLevel(raw, ',')
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = normalizeSQL(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result, nil
}

func splitTopLevelPair(raw string, separator byte) (string, string, bool, error) {
	parts, err := querybuilder.SplitTopLevel(raw, separator)
	if err != nil {
		return "", "", false, err
	}
	if len(parts) < 2 {
		return "", "", false, nil
	}

	left := parts[0]
	right := strings.Join(parts[1:], string(separator))
	return left, right, true, nil
}

func columnPositionFor(previousName string) *querybuilder.ColumnPosition {
	if previousName == "" {
		return querybuilder.FirstColumnPosition()
	}
	return querybuilder.AfterColumnPosition(previousName)
}

func extractColumnExpression(column dbops.Column) columnExpression {
	switch {
	case column.DefaultExpression != nil:
		return columnExpression{kind: querybuilder.ColumnExpressionKindDefault, sql: *column.DefaultExpression}
	case column.MaterializedExpression != nil:
		return columnExpression{kind: querybuilder.ColumnExpressionKindMaterialized, sql: *column.MaterializedExpression}
	case column.AliasExpression != nil:
		return columnExpression{kind: querybuilder.ColumnExpressionKindAlias, sql: *column.AliasExpression}
	case column.EphemeralExpression != nil:
		return columnExpression{kind: querybuilder.ColumnExpressionKindEphemeral, sql: *column.EphemeralExpression}
	default:
		return columnExpression{}
	}
}

func collectIdentifierReferences(current dbops.Table, desired dbops.Table) map[string]bool {
	referenced := make(map[string]bool)
	for _, expression := range collectExpressions(current, desired) {
		for _, name := range extractIdentifiers(expression) {
			referenced[name] = true
		}
	}
	return referenced
}

func collectExpressions(tables ...dbops.Table) []string {
	expressions := make([]string, 0)
	for _, table := range tables {
		if value := normalizeSQL(table.PartitionBy); value != "" {
			expressions = append(expressions, value)
		}
		if value := normalizeSQL(table.OrderBy); value != "" {
			expressions = append(expressions, value)
		}
		if value := normalizeSQL(table.PrimaryKey); value != "" {
			expressions = append(expressions, value)
		}
		if value := normalizeSQL(table.SampleBy); value != "" {
			expressions = append(expressions, value)
		}
		if value := normalizeSQL(table.TTL); value != "" {
			expressions = append(expressions, value)
		}
		for _, column := range table.Columns {
			if column.DefaultExpression != nil {
				expressions = append(expressions, normalizeSQL(*column.DefaultExpression))
			}
			if column.MaterializedExpression != nil {
				expressions = append(expressions, normalizeSQL(*column.MaterializedExpression))
			}
			if column.AliasExpression != nil {
				expressions = append(expressions, normalizeSQL(*column.AliasExpression))
			}
			if column.EphemeralExpression != nil {
				expressions = append(expressions, normalizeSQL(*column.EphemeralExpression))
			}
		}
	}
	return expressions
}

var (
	identifierPattern         = regexp.MustCompile("`[^`]+`|[A-Za-z_][A-Za-z0-9_]*")
	ttlIntervalFuncPattern    = regexp.MustCompile(`(?i)\btoInterval([A-Za-z]+)\s*\(\s*([^)]+?)\s*\)`)
	ttlIntervalKeywordPattern = regexp.MustCompile(`(?i)\bINTERVAL\s+([^\s,()]+)\s+([A-Za-z]+)`)
)

func extractIdentifiers(expression string) []string {
	result := make([]string, 0)
	seen := make(map[string]struct{})
	state := querybuilder.SQLScanState{}

	for index := 0; index < len(expression); index++ {
		if state.InQuote != 0 {
			next, err := querybuilder.AdvanceSQLScanState(expression, index, &state)
			if err != nil {
				return result
			}
			index = next
			continue
		}

		ch := expression[index]
		if ch == '`' {
			start := index
			next, err := querybuilder.AdvanceSQLScanState(expression, index, &state)
			if err != nil {
				return result
			}
			if state.InQuote == 0 {
				name := querybuilder.UnquoteIdentifier(expression[start : next+1])
				if name != "" {
					if _, ok := seen[name]; !ok {
						seen[name] = struct{}{}
						result = append(result, name)
					}
				}
			}
			index = next
			continue
		}

		if isIdentifierStart(ch) {
			start := index
			for index+1 < len(expression) && isIdentifierPart(expression[index+1]) {
				index++
			}
			name := expression[start : index+1]
			if !isFunctionLikeIdentifier(expression, index+1) {
				if _, ok := seen[name]; !ok {
					seen[name] = struct{}{}
					result = append(result, name)
				}
			}
			continue
		}

		next, err := querybuilder.AdvanceSQLScanState(expression, index, &state)
		if err != nil {
			return result
		}
		index = next
	}

	return result
}

func simpleIdentifier(expression string) (string, bool) {
	expression = normalizeSQL(expression)
	if expression == "" {
		return "", false
	}
	unquoted := querybuilder.UnquoteIdentifier(expression)
	if unquoted == "" {
		return "", false
	}
	if !identifierPattern.MatchString(unquoted) || identifierPattern.FindString(unquoted) != unquoted {
		return "", false
	}
	return unquoted, true
}

func isIdentifierStart(ch byte) bool {
	return ch == '_' || ('A' <= ch && ch <= 'Z') || ('a' <= ch && ch <= 'z')
}

func isIdentifierPart(ch byte) bool {
	return isIdentifierStart(ch) || ('0' <= ch && ch <= '9')
}

func isFunctionLikeIdentifier(expression string, index int) bool {
	for index < len(expression) {
		r, size := utf8.DecodeRuneInString(expression[index:])
		if r == utf8.RuneError && size == 1 {
			break
		}
		if !unicode.IsSpace(r) {
			return r == '('
		}
		index += size
	}
	return false
}

func unwrapOuterParens(value string) string {
	value = normalizeSQL(value)
	if len(value) < 2 || value[0] != '(' || value[len(value)-1] != ')' {
		return value
	}

	state := querybuilder.SQLScanState{}
	for index := 0; index < len(value); index++ {
		ch := value[index]
		var err error
		index, err = querybuilder.AdvanceSQLScanState(value, index, &state)
		if err != nil {
			return value
		}
		if ch == ')' && state.ParenDepth == 0 && index != len(value)-1 {
			return value
		}
	}

	return normalizeSQL(value[1 : len(value)-1])
}

func normalizeSQL(value string) string {
	return querybuilder.NormalizeSQL(value)
}

// planNamedChanges compares two lists of named definitions (indexes, projections,
// constraints) by name, not by position. A changed definition is dropped and added again.
func planNamedChanges[T any](current []T, desired []T, name func(T) string, equal func(T, T) bool, add func(T) string, drop func(string) string) ([]string, []string) {
	currentByName := make(map[string]T, len(current))
	for _, item := range current {
		currentByName[name(item)] = item
	}

	var drops, adds []string
	desiredNames := make(map[string]struct{}, len(desired))
	for _, item := range desired {
		desiredNames[name(item)] = struct{}{}
		existing, exists := currentByName[name(item)]
		if exists && equal(existing, item) {
			continue
		}
		if exists {
			drops = append(drops, drop(name(item)))
		}
		adds = append(adds, add(item))
	}
	for _, item := range current {
		if _, ok := desiredNames[name(item)]; !ok {
			drops = append(drops, drop(name(item)))
		}
	}

	return drops, adds
}

// namedListsEqual compares two lists of named definitions by name, not by position.
func namedListsEqual[T any](left []T, right []T, name func(T) string, equal func(T, T) bool) bool {
	drops, adds := planNamedChanges(left, right, name, equal, func(T) string { return "" }, func(string) string { return "" })
	return len(left) == len(right) && len(drops)+len(adds) == 0
}

func indexName(index dbops.Index) string { return index.Name }

func indexEqual(left dbops.Index, right dbops.Index) bool {
	return expressionsEqual(left.Expression, right.Expression) && expressionsEqual(left.Type, right.Type) &&
		max(left.Granularity, 1) == max(right.Granularity, 1)
}

func projectionName(projection dbops.Projection) string { return projection.Name }

func projectionEqual(left dbops.Projection, right dbops.Projection) bool {
	return expressionsEqual(left.Query, right.Query) && expressionsEqual(left.Settings, right.Settings)
}

func constraintName(constraint dbops.Constraint) string { return constraint.Name }

func constraintEqual(left dbops.Constraint, right dbops.Constraint) bool {
	return expressionsEqual(left.Check, right.Check)
}

// filterUnmanaged removes the remote columns and indexes that match an unmanaged pattern
// and are not declared in the configuration, so the provider never reports or changes them.
func filterUnmanaged(remote dbops.Table, declared dbops.Table, columnPatterns []*regexp.Regexp, indexPatterns []*regexp.Regexp) dbops.Table {
	declaredColumns := make(map[string]struct{}, len(declared.Columns))
	for _, column := range declared.Columns {
		declaredColumns[column.Name] = struct{}{}
	}
	declaredIndexes := make(map[string]struct{}, len(declared.Indexes))
	for _, index := range declared.Indexes {
		declaredIndexes[index.Name] = struct{}{}
	}

	unmanaged := func(name string, declared map[string]struct{}, patterns []*regexp.Regexp) bool {
		if _, ok := declared[name]; ok {
			return false
		}
		return slices.ContainsFunc(patterns, func(pattern *regexp.Regexp) bool { return pattern.MatchString(name) })
	}

	remote.Columns = slices.DeleteFunc(slices.Clone(remote.Columns), func(column dbops.Column) bool {
		return unmanaged(column.Name, declaredColumns, columnPatterns)
	})
	remote.Indexes = slices.DeleteFunc(slices.Clone(remote.Indexes), func(index dbops.Index) bool {
		return unmanaged(index.Name, declaredIndexes, indexPatterns)
	})

	return remote
}

func columnsEqualIgnoringName(left dbops.Column, right dbops.Column) bool {
	left.Name = ""
	right.Name = ""
	return schemahelpers.ColumnEqual(left, right)
}

func hasDuplicateColumnNames(columns []dbops.Column) bool {
	seen := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		if _, exists := seen[column.Name]; exists {
			return true
		}
		seen[column.Name] = struct{}{}
	}
	return false
}

func columnInDesiredPosition(order []string, name string, previousName string) bool {
	index := indexOf(order, name)
	if index == -1 {
		return false
	}
	if previousName == "" {
		return index == 0
	}
	prevIndex := indexOf(order, previousName)
	return prevIndex != -1 && index == prevIndex+1
}

func insertNameAfter(order []string, name string, previousName string) []string {
	order = append([]string{}, order...)
	if previousName == "" {
		return append([]string{name}, order...)
	}
	index := indexOf(order, previousName)
	if index == -1 {
		return append(order, name)
	}
	order = append(order[:index+1], append([]string{name}, order[index+1:]...)...)
	return order
}

func moveNameAfter(order []string, name string, previousName string) []string {
	order = append([]string{}, order...)
	index := indexOf(order, name)
	if index == -1 {
		return order
	}
	order = append(order[:index], order[index+1:]...)
	return insertNameAfter(order, name, previousName)
}

func indexOf(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}
