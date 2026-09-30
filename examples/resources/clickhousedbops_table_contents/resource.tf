resource "clickhousedbops_table" "country" {
  database = "analytics"
  name     = "country"
  engine   = "MergeTree()"
  order_by = "code"
  columns = [
    { name = "code", type = "String" },
    { name = "name", type = "String" },
  ]
}

# countries.csv:
#   code,name
#   DE,Germany
#   FR,France
resource "clickhousedbops_table_contents" "country" {
  database = clickhousedbops_table.country.database
  table    = clickhousedbops_table.country.name
  format   = "CSVWithNames"
  data     = file("${path.module}/countries.csv")
}
