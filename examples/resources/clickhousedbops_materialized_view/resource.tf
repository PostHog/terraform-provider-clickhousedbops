locals {
  daily_count_columns = [
    { name = "team_id", type = "UInt64", nullable = false },
    { name = "event_date", type = "Date", nullable = false },
    { name = "event_count", type = "UInt64", nullable = false },
  ]
}

resource "clickhousedbops_table" "daily_event_counts" {
  database = "analytics"
  name     = "daily_event_counts"
  engine   = "MergeTree()"
  order_by = "(team_id, event_date)"
  columns  = local.daily_count_columns
}

resource "clickhousedbops_materialized_view" "events_daily_mv" {
  database   = "analytics"
  name       = "events_daily_mv"
  to_table   = clickhousedbops_table.daily_event_counts.qualified_name
  to_columns = local.daily_count_columns
  query      = <<-SQL
    SELECT team_id, toDate(created_at) AS event_date, count() AS event_count
    FROM analytics.events
    GROUP BY team_id, event_date
  SQL
}
