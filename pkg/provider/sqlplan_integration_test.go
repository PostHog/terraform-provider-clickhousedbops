package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/clickhouseclient"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
)

// This opt-in test uses separate CLI/provider processes and an isolated temporary database.
func TestSQLPlanSavedFile(t *testing.T) {
	endpoint := os.Getenv("SQL_PLAN_TEST_CLICKHOUSE_URL")
	if endpoint == "" {
		t.Skip("set SQL_PLAN_TEST_CLICKHOUSE_URL to run against a disposable ClickHouse instance")
	}
	cli := os.Getenv("TF_ACC_TERRAFORM_PATH")
	if cli == "" {
		cli = "tofu"
	}
	upstream, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	database := fmt.Sprintf("sql_plan_test_%d", time.Now().UnixNano())
	queryEndpoint := func(endpoint, sql string) string {
		t.Helper()
		resp, err := http.Post(endpoint, "text/plain", strings.NewReader(sql)) //nolint:gosec // The test explicitly selects a disposable database endpoint.
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Error(err)
			}
		}()
		data, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("ClickHouse: %s: %v", data, err)
		}
		return string(data)
	}
	query := func(sql string) string { return queryEndpoint(endpoint, sql) }
	query("CREATE DATABASE " + database)
	t.Cleanup(func() { query("DROP DATABASE " + database + " SYNC") })
	query("CREATE TABLE " + database + ".sink (id UInt64) ENGINE = Memory")
	port, err := strconv.ParseUint(upstream.Port(), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := clickhouseclient.NewHTTPClient(clickhouseclient.HTTPClientConfig{Protocol: upstream.Scheme, Host: upstream.Hostname(), Port: uint16(port), BasicAuth: &clickhouseclient.BasicAuth{Username: "default"}})
	if err != nil {
		t.Fatal(err)
	}
	previewClient, err := dbops.NewClient(clickhouseclient.WithSQLPlan(raw, endpoint))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "tuple()", "id", "(id, length(extra))", "(id, tuple(extra))"} {
		partitionClause := ""
		if key != "" {
			partitionClause = " PARTITION BY " + key
		}
		query("CREATE TABLE " + database + ".partition_probe (id UInt64, extra String) ENGINE MergeTree" + partitionClause + " ORDER BY id")
		query("INSERT INTO " + database + ".partition_probe VALUES (1,'invented')")
		actualID := strings.TrimSpace(query("SELECT _partition_id FROM " + database + ".partition_probe"))
		ctx, recording := clickhouseclient.RecordSQL(context.Background())
		if err := previewClient.ReplaceTableContents(ctx, database, "partition_probe", "JSONEachRow", `{"id":1,"extra":"invented"}`); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, operation := range recording.Operations {
			if strings.Contains(operation.SQL, "REPLACE PARTITION ID '"+actualID+"'") {
				found = true
			}
		}
		if !found {
			t.Fatalf("partition key %q preview does not match actual ID %q: %#v", key, actualID, recording.Operations)
		}
		query("DROP TABLE " + database + ".partition_probe SYNC")
	}

	secondary := os.Getenv("SQL_PLAN_TEST_SECOND_CLICKHOUSE_URL")
	var secondaryProxy *httputil.ReverseProxy
	if secondary != "" {
		secondURL, err := url.Parse(secondary)
		if err != nil {
			t.Fatal(err)
		}
		secondaryProxy = httputil.NewSingleHostReverseProxy(secondURL) //nolint:gosec // Explicit disposable test endpoint.
		queryEndpoint(secondary, "CREATE DATABASE "+database)
		t.Cleanup(func() { queryEndpoint(secondary, "DROP DATABASE "+database+" SYNC") })
		queryEndpoint(secondary, "CREATE TABLE "+database+".sink (id UInt64) ENGINE Memory")
	}

	var mu sync.Mutex
	var writes []clickhouseclient.SQLOperation
	proxy := httputil.NewSingleHostReverseProxy(upstream) //nolint:gosec // Opt-in test endpoint, never a production request.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
		sql := string(data)
		if secondaryProxy != nil && strings.Contains(strings.ReplaceAll(sql, "`", ""), "FROM system.clusters") {
			_, err := io.WriteString(w, `{"meta":[{"name":"host_name","type":"String"},{"name":"host_address","type":"String"},{"name":"port","type":"UInt64"},{"name":"shard_num","type":"UInt64"},{"name":"replica_num","type":"UInt64"}],"data":[["node1","127.0.0.1","9000","1","1"],["node2","localhost","9000","2","1"]]}`)
			if err != nil {
				t.Error(err)
			}
			return
		}

		if !strings.HasPrefix(sql, "SELECT ") && !strings.HasPrefix(sql, "SHOW ") && !strings.HasPrefix(sql, "EXISTS ") {
			var params map[string]string
			for key, values := range r.URL.Query() {
				if strings.HasPrefix(key, "param_") {
					if params == nil {
						params = map[string]string{}
					}
					params[strings.TrimPrefix(key, "param_")] = values[0]
				}
			}
			mu.Lock()
			writes = append(writes, clickhouseclient.SQLOperation{SQL: sql, Parameters: params})
			mu.Unlock()
		}
		if secondaryProxy != nil && strings.HasPrefix(r.Host, "localhost:") {
			secondaryProxy.ServeHTTP(w, r)
		} else {
			proxy.ServeHTTP(w, r)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(binDir, "terraform-provider-clickhousedbops"), "../..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", output, err)
	}
	rc := filepath.Join(dir, "terraformrc")
	if err := os.WriteFile(rc, []byte(fmt.Sprintf("provider_installation { dev_overrides { \"registry.terraform.io/ClickHouse/clickhousedbops\" = %q } direct {} }", binDir)), 0o600); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(server.URL)
	header := fmt.Sprintf(`terraform {
 required_providers {
  clickhousedbops = { source = "registry.terraform.io/ClickHouse/clickhousedbops" }
 }
}
provider "clickhousedbops" {
 protocol = "http"
 host = "127.0.0.1"
 port = %s
 auth_config = { strategy = "basicauth", username = "default" }
 enforce_sql_plan = true
}
`, u.Port())
	if secondaryProxy != nil {
		header = strings.Replace(header, "enforce_sql_plan = true", "enforce_sql_plan = true\n fanout_cluster = \"sql_plan_test\"", 1)
	}
	table := fmt.Sprintf(`resource "clickhousedbops_table" "events" {
 database = %q
 name = "events"
 engine = "MergeTree()"
 order_by = "id"
 columns = [{ name = "id", type = "UInt64" }]
 force_destroy = true
}
`, database)
	objects := fmt.Sprintf(`resource "clickhousedbops_view" "events" {
 database = %q
 name = "events_view"
 query = "SELECT id FROM %s.events"
 depends_on = [clickhousedbops_table.events]
}
resource "clickhousedbops_materialized_view" "events" {
 database = %q
 name = "events_mv"
 to_table = "%s.sink"
 query = "SELECT id FROM %s.events"
 depends_on = [clickhousedbops_table.events]
}
resource "clickhousedbops_dictionary" "ids" {
 database = %q
 name = "ids"
 attributes = [{ name = "id", type = "UInt64" }, { name = "value", type = "String" }]
 primary_key = ["id"]
 source = "NULL()"
 layout = "HASHED()"
 lifetime = "MIN 0 MAX 0"
}
`, database, database, database, database, database, database)
	writeConfig := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(header+body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, cli, args...) //nolint:gosec // Explicit test CLI path with fixed arguments.
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "TF_CLI_CONFIG_FILE="+rc, "TF_IN_AUTOMATION=1")
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	mustRun := func(args ...string) string {
		t.Helper()
		output, err := run(args...)
		if err != nil {
			t.Fatalf("%v: %s: %v", args, output, err)
		}
		return output
	}
	snapshot := func() []clickhouseclient.SQLOperation {
		mu.Lock()
		defer mu.Unlock()
		return append([]clickhouseclient.SQLOperation{}, writes...)
	}
	plan := func(extra ...string) {
		t.Helper()
		before := len(snapshot())
		args := append([]string{"plan", "-no-color", "-out=reviewed.tfplan"}, extra...)
		output := mustRun(args...)
		if len(snapshot()) != before {
			t.Fatalf("planning sent writes: %#v", snapshot()[before:])
		}
		if !strings.Contains(output, "Reviewed SQL execution plan") {
			t.Fatalf("plan did not print SQL: %s", output)
		}
	}
	apply := func() { t.Helper(); mustRun("apply", "-no-color", "reviewed.tfplan") }
	writeConfig(table + objects)
	plan()
	jsonPlan := mustRun("show", "-json", "reviewed.tfplan")
	var shown struct {
		PlannedValues struct {
			RootModule struct {
				Resources []struct {
					Values struct {
						SQLPlan string `json:"sql_plan"`
					} `json:"values"`
				} `json:"resources"`
			} `json:"root_module"`
		} `json:"planned_values"`
	}
	if err := json.Unmarshal([]byte(jsonPlan[strings.Index(jsonPlan, "{"):]), &shown); err != nil {
		t.Fatal(err)
	}
	var reviewed []clickhouseclient.SQLOperation
	for _, resource := range shown.PlannedValues.RootModule.Resources {
		var operations []clickhouseclient.SQLOperation
		if err := json.Unmarshal([]byte(resource.Values.SQLPlan), &operations); err != nil {
			t.Fatal(err)
		}
		reviewed = append(reviewed, operations...)
	}
	apply()
	for _, actual := range snapshot() {
		found := false
		for _, operation := range reviewed {
			if operation.SQL == actual.SQL || strings.Contains(operation.SQL, "SOURCE([REDACTED])") && strings.Replace(operation.SQL, "SOURCE([REDACTED])", "SOURCE(NULL())", 1) == actual.SQL {
				found = true
			}
		}
		if !found {
			t.Fatalf("executed SQL missing from printout: %s", actual.SQL)
		}
	}
	// Changes are bound to the saved file, despite configuration edits after planning.
	updated := strings.Replace(table, `columns = [{ name = "id", type = "UInt64" }]`, `columns = [{ name = "id", type = "UInt64" }, { name = "extra", type = "String" }]`, 1)
	writeConfig(updated + objects)
	plan()
	writeConfig(strings.Replace(updated, `name = "extra"`, `name = "unreviewed"`, 1) + objects)
	apply()
	if result := query("SELECT name FROM system.columns WHERE database = '" + database + "' AND table = 'events' ORDER BY position"); result != "id\nextra\n" {
		t.Fatalf("saved plan ignored: %q", result)
	}
	writeConfig(updated + objects)
	output := mustRun("plan", "-no-color")
	if !strings.Contains(output, "No changes.") {
		t.Fatalf("applied configuration is not a no-op: %s", output)
	}
	// Empty replacements must preserve both the reviewed drop and create.
	replaced := strings.Replace(updated, `order_by = "id"`, `order_by = "id"
 partition_by = "id"`, 1)
	writeConfig(replaced + objects)
	plan()
	apply()
	// A table-contents plan reviews staging, bound data, partition swaps and cleanup.
	contents := fmt.Sprintf(`resource "clickhousedbops_table_contents" "events" {
 database = %q
 table = "events"
 format = "JSONEachRow"
 data = "{\"id\":1,\"extra\":\"invented fixture\"}"
}
`, database)
	writeConfig(replaced + objects + contents)
	plan()
	apply()
	if result := query("SELECT count() FROM " + database + ".events"); result != "1\n" {
		t.Fatalf("contents not applied: %q", result)
	}
	if secondaryProxy != nil {
		if result := queryEndpoint(secondary, "SELECT count() FROM "+database+".events"); result != "1\n" {
			t.Fatalf("fanout missed second node: %q", result)
		}
	}
	// Apply-time live drift must never invent a compensating ADD COLUMN.
	changed := strings.Replace(replaced, `{ name = "extra", type = "String" }`, `{ name = "extra", type = "String" }, { name = "reviewed", type = "String" }`, 1)
	writeConfig(changed + objects)
	plan()
	query("ALTER TABLE " + database + ".events DROP COLUMN extra")
	if secondaryProxy != nil {
		queryEndpoint(secondary, "ALTER TABLE "+database+".events DROP COLUMN extra")
	}
	before := len(snapshot())
	output, err = run("apply", "-no-color", "reviewed.tfplan")
	if err == nil {
		t.Fatal("drifted apply succeeded")
	}
	if len(snapshot()) != before {
		t.Fatalf("drifted apply wrote SQL before rejection: %s", output)
	}
	// Destroy plans have null planned state; private data still enforces every drop.
	writeConfig(changed + objects)
	plan("-destroy")
	apply()
	if result := query("SELECT count() FROM system.tables WHERE database = '" + database + "' AND name != 'sink'"); result != "0\n" {
		t.Fatalf("destroy left objects: %q", result)
	}
}
