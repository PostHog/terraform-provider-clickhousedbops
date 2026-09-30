package dbops

import (
	"context"
	"strings"

	"github.com/pingcap/errors"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/clickhouseclient"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/querybuilder"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/tableengine"
)

type TableEngineCapabilities struct {
	Name              string
	Known             bool
	SupportsSettings  bool
	SupportsSortOrder bool
	SupportsTTL       bool
}

type TableSettingCapability struct {
	Name     string
	Known    bool
	Readonly bool
	// Default is the value that the server gives to a table that does not set the setting.
	Default string
}

func (i *impl) GetTableEngineCapabilities(ctx context.Context, engine string) (TableEngineCapabilities, error) {
	name := tableengine.BaseName(engine)
	if name == "" {
		return TableEngineCapabilities{}, errors.New("table engine cannot be empty")
	}

	sql, err := querybuilder.NewSelect(
		[]querybuilder.Field{
			querybuilder.NewField("name"),
			querybuilder.NewField("supports_settings"),
			querybuilder.NewField("supports_sort_order"),
			querybuilder.NewField("supports_ttl"),
		},
		"system.table_engines",
	).Where(querybuilder.WhereEquals("name", name)).Build()
	if err != nil {
		return TableEngineCapabilities{}, errors.WithMessage(err, "error building query")
	}

	result := TableEngineCapabilities{Name: name}
	err = i.clickhouseClient.Select(ctx, sql, func(data clickhouseclient.Row) error {
		var rowErr error

		result.Name, rowErr = data.GetString("name")
		if rowErr != nil {
			return rowErr
		}
		result.Known = true
		result.SupportsSettings, rowErr = data.GetBool("supports_settings")
		if rowErr != nil {
			return rowErr
		}
		result.SupportsSortOrder, rowErr = data.GetBool("supports_sort_order")
		if rowErr != nil {
			return rowErr
		}
		result.SupportsTTL, rowErr = data.GetBool("supports_ttl")
		return rowErr
	})
	if err != nil {
		return TableEngineCapabilities{}, errors.WithMessage(err, "error running query")
	}

	return result, nil
}

func (i *impl) GetTableSettingCapabilities(ctx context.Context, engine string, settingNames []string) (map[string]TableSettingCapability, error) {
	capabilities := make(map[string]TableSettingCapability, len(settingNames))
	if len(settingNames) == 0 {
		return capabilities, nil
	}

	family := tableengine.FamilyForEngine(engine)
	if family != tableengine.FamilyMergeTree {
		return capabilities, nil
	}

	for _, name := range settingNames {
		capabilities[name] = TableSettingCapability{Name: name}
	}

	baseName := tableengine.BaseName(engine)
	tables := []string{"system.merge_tree_settings"}
	if strings.HasPrefix(strings.ToLower(baseName), "replicated") {
		tables = append(tables, "system.replicated_merge_tree_settings")
	}

	for _, tableName := range tables {
		sql, err := querybuilder.NewSelect(
			[]querybuilder.Field{
				querybuilder.NewField("name"),
				querybuilder.NewField("readonly"),
				querybuilder.NewField("value"),
			},
			tableName,
		).Where(querybuilder.WhereIn("name", settingNames)).Build()
		if err != nil {
			return nil, errors.WithMessage(err, "error building query")
		}

		err = i.clickhouseClient.Select(ctx, sql, func(data clickhouseclient.Row) error {
			name, rowErr := data.GetString("name")
			if rowErr != nil {
				return rowErr
			}
			readonly, rowErr := data.GetBool("readonly")
			if rowErr != nil {
				return rowErr
			}

			value, rowErr := data.GetString("value")
			if rowErr != nil {
				return rowErr
			}

			capabilities[name] = TableSettingCapability{
				Name:     name,
				Known:    true,
				Readonly: readonly,
				Default:  value,
			}
			return nil
		})
		if err != nil {
			return nil, errors.WithMessage(err, "error running query")
		}
	}

	return capabilities, nil
}
