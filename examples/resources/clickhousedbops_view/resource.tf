locals {
  view_columns = [
    { name = "team_id", type = "UInt64", nullable = false },
    { name = "event_count", type = "UInt64", nullable = false },
  ]
}

resource "clickhousedbops_view" "team_event_counts" {
  database = "analytics"
  name     = "team_event_counts"
  columns  = local.view_columns
  query    = <<-SQL
    SELECT team_id, count() AS event_count
    FROM analytics.events
    GROUP BY team_id
  SQL
}
