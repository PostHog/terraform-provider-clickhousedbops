package dbops

import "testing"

func TestParseCreateDictionaryDefinition_Basic(t *testing.T) {
	stmt := "CREATE DICTIONARY `mydb`.`mydict` (`id` UInt64, `name` String) PRIMARY KEY id SOURCE(CLICKHOUSE(TABLE 'source_table' DB 'mydb')) LIFETIME(MIN 0 MAX 300) LAYOUT(FLAT())"
	definition, err := parseCreateDictionaryDefinition(stmt)
	if err != nil {
		t.Fatalf("parseCreateDictionaryDefinition() error = %v", err)
	}

	if len(definition.Attributes) != 2 {
		t.Fatalf("expected 2 attributes, got %d", len(definition.Attributes))
	}
	if definition.Attributes[0].Name != "id" || definition.Attributes[0].Type != "UInt64" {
		t.Fatalf("unexpected first attribute: %#v", definition.Attributes[0])
	}
	if definition.Attributes[1].Name != "name" || definition.Attributes[1].Type != "String" {
		t.Fatalf("unexpected second attribute: %#v", definition.Attributes[1])
	}

	if len(definition.PrimaryKey) != 1 || definition.PrimaryKey[0] != "id" {
		t.Fatalf("unexpected primary key: %v", definition.PrimaryKey)
	}

	if definition.Source != "CLICKHOUSE(TABLE 'source_table' DB 'mydb')" {
		t.Fatalf("unexpected source: %q", definition.Source)
	}
	if definition.Lifetime != "MIN 0 MAX 300" {
		t.Fatalf("unexpected lifetime: %q", definition.Lifetime)
	}
	if definition.Layout != "FLAT()" {
		t.Fatalf("unexpected layout: %q", definition.Layout)
	}
}

func TestParseCreateDictionaryDefinition_WithModifiers(t *testing.T) {
	stmt := "CREATE DICTIONARY `mydb`.`mydict` (`id` UInt64, `parent_id` UInt64 DEFAULT 0 HIERARCHICAL, `label` String INJECTIVE, `computed` String EXPRESSION toString(id)) PRIMARY KEY id SOURCE(CLICKHOUSE(TABLE 'src' DB 'mydb')) LIFETIME(MIN 0 MAX 600) LAYOUT(HASHED())"
	definition, err := parseCreateDictionaryDefinition(stmt)
	if err != nil {
		t.Fatalf("parseCreateDictionaryDefinition() error = %v", err)
	}

	if len(definition.Attributes) != 4 {
		t.Fatalf("expected 4 attributes, got %d", len(definition.Attributes))
	}

	// parent_id: DEFAULT 0, HIERARCHICAL
	parentID := definition.Attributes[1]
	if parentID.Name != "parent_id" {
		t.Fatalf("unexpected attribute name: %q", parentID.Name)
	}
	if parentID.DefaultExpression == nil || *parentID.DefaultExpression != "0" {
		t.Fatalf("expected DEFAULT '0', got %v", parentID.DefaultExpression)
	}
	if !parentID.Hierarchical {
		t.Fatalf("expected HIERARCHICAL to be true")
	}

	// label: INJECTIVE
	label := definition.Attributes[2]
	if label.Name != "label" {
		t.Fatalf("unexpected attribute name: %q", label.Name)
	}
	if !label.Injective {
		t.Fatalf("expected INJECTIVE to be true")
	}

	// computed: EXPRESSION toString(id)
	computed := definition.Attributes[3]
	if computed.Name != "computed" {
		t.Fatalf("unexpected attribute name: %q", computed.Name)
	}
	if computed.Expression == nil || *computed.Expression != "toString(id)" {
		t.Fatalf("expected EXPRESSION 'toString(id)', got %v", computed.Expression)
	}
}

func TestParseCreateDictionaryDefinition_NullableType(t *testing.T) {
	stmt := "CREATE DICTIONARY `mydb`.`mydict` (`id` UInt64, `value` Nullable(Float64)) PRIMARY KEY id SOURCE(CLICKHOUSE(TABLE 'src' DB 'mydb')) LIFETIME(MIN 0 MAX 100) LAYOUT(FLAT())"
	definition, err := parseCreateDictionaryDefinition(stmt)
	if err != nil {
		t.Fatalf("parseCreateDictionaryDefinition() error = %v", err)
	}

	if len(definition.Attributes) != 2 {
		t.Fatalf("expected 2 attributes, got %d", len(definition.Attributes))
	}

	value := definition.Attributes[1]
	if value.Name != "value" {
		t.Fatalf("unexpected attribute name: %q", value.Name)
	}
	if value.Type != "Nullable(Float64)" {
		t.Fatalf("expected verbatim type Nullable(Float64), got %q", value.Type)
	}
	if value.Nullable {
		t.Fatalf("expected Nullable to stay false for a verbatim type")
	}
}

func TestParseCreateDictionaryDefinition_Empty(t *testing.T) {
	definition, err := parseCreateDictionaryDefinition("")
	if err != nil {
		t.Fatalf("parseCreateDictionaryDefinition() error = %v", err)
	}

	if len(definition.Attributes) != 0 {
		t.Fatalf("expected no attributes, got %d", len(definition.Attributes))
	}
	if len(definition.PrimaryKey) != 0 {
		t.Fatalf("expected no primary key, got %v", definition.PrimaryKey)
	}
	if definition.Source != "" {
		t.Fatalf("expected empty source, got %q", definition.Source)
	}
	if definition.Lifetime != "" {
		t.Fatalf("expected empty lifetime, got %q", definition.Lifetime)
	}
	if definition.Layout != "" {
		t.Fatalf("expected empty layout, got %q", definition.Layout)
	}
	if definition.Settings != "" {
		t.Fatalf("expected empty settings, got %q", definition.Settings)
	}
}

func TestParseCreateDictionaryDefinition_WithSettings(t *testing.T) {
	stmt := "CREATE DICTIONARY `mydb`.`mydict` (`id` UInt64, `name` String) PRIMARY KEY id SOURCE(CLICKHOUSE(TABLE 'src' DB 'mydb')) LIFETIME(MIN 0 MAX 300) LAYOUT(COMPLEX_KEY_HASHED()) SETTINGS max_threads = 4"
	definition, err := parseCreateDictionaryDefinition(stmt)
	if err != nil {
		t.Fatalf("parseCreateDictionaryDefinition() error = %v", err)
	}

	if definition.Layout != "COMPLEX_KEY_HASHED()" {
		t.Fatalf("unexpected layout: %q", definition.Layout)
	}

	if definition.Settings != "max_threads = 4" {
		t.Fatalf("unexpected settings: %q", definition.Settings)
	}
}

func TestParseCreateDictionaryDefinition_RangeAndQuerySource(t *testing.T) {
	definition, err := parseCreateDictionaryDefinition("CREATE DICTIONARY db.rates\n(\n    `currency` String,\n    `start_date` Date,\n    `end_date` Nullable(Date),\n    `rate` Decimal64(10)\n)\n" +
		"PRIMARY KEY currency\n" +
		"SOURCE(CLICKHOUSE(QUERY 'SELECT a, \\'x\\' AS b,\\n c FROM `db`.`t` WHERE s != \\'LAYOUT(\\'' USER 'default' PASSWORD '[HIDDEN]'))\n" +
		"LIFETIME(MIN 3000 MAX 3600)\n" +
		"LAYOUT(COMPLEX_KEY_RANGE_HASHED(RANGE_LOOKUP_STRATEGY 'max'))\n" +
		"RANGE(MIN start_date MAX end_date)")
	if err != nil {
		t.Fatalf("parseCreateDictionaryDefinition() error = %v", err)
	}

	if want := "CLICKHOUSE(QUERY 'SELECT a, \\'x\\' AS b,\\n c FROM `db`.`t` WHERE s != \\'LAYOUT(\\'' USER 'default' PASSWORD '[HIDDEN]')"; definition.Source != want {
		t.Errorf("source = %q, want %q", definition.Source, want)
	}
	if definition.Lifetime != "MIN 3000 MAX 3600" {
		t.Errorf("unexpected lifetime: %q", definition.Lifetime)
	}
	if definition.Layout != "COMPLEX_KEY_RANGE_HASHED(RANGE_LOOKUP_STRATEGY 'max')" {
		t.Errorf("unexpected layout: %q", definition.Layout)
	}
	if definition.Range != "MIN start_date MAX end_date" {
		t.Errorf("unexpected range: %q", definition.Range)
	}
	if len(definition.Attributes) != 4 || definition.Attributes[2].Type != "Nullable(Date)" || definition.Attributes[2].Nullable {
		t.Errorf("unexpected attributes: %#v", definition.Attributes)
	}
}
