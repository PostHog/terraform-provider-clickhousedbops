locals {
  event_columns = [
    { name = "team_id", type = "UInt64", nullable = false },
    { name = "event", type = "String", nullable = false },
    { name = "created_at", type = "DateTime", nullable = false, default_expression = "now()" },
  ]
}

resource "clickhousedbops_table" "events_local" {
  cluster_name = "cluster"
  database     = "analytics"
  name         = "events_local"
  engine       = "MergeTree()"
  partition_by = "toYYYYMM(created_at)"
  order_by     = "(team_id, created_at)"
  columns      = local.event_columns
}

resource "clickhousedbops_table" "events" {
  cluster_name = "cluster"
  database     = "analytics"
  name         = "events"
  engine       = "Distributed('cluster', 'analytics', '${clickhousedbops_table.events_local.name}', rand())"
  columns      = local.event_columns
}
