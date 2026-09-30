locals {
  dictionary_source_columns = [
    { name = "id", type = "UInt64", nullable = false },
    { name = "value", type = "String", nullable = false },
  ]
  dictionary_attributes = [
    { name = "id", type = "UInt64", nullable = false },
    { name = "value", type = "String", nullable = false, default_expression = "'unknown'" },
  ]
}

resource "clickhousedbops_table" "dictionary_source" {
  database = "analytics"
  name     = "dictionary_source"
  engine   = "MergeTree()"
  order_by = "id"
  columns  = local.dictionary_source_columns
}

resource "clickhousedbops_dictionary" "teams" {
  database    = "analytics"
  name        = "teams"
  attributes  = local.dictionary_attributes
  primary_key = ["id"]
  source      = "CLICKHOUSE(HOST 'localhost' PORT tcpPort() USER 'default' PASSWORD 'test' DB 'analytics' TABLE 'dictionary_source')"
  layout      = "FLAT()"
  lifetime    = "0"
}
