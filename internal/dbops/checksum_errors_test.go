package dbops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/clickhouseclient"
)

type contentsServer struct {
	clickhouseclient.ClickhouseClient
	exists bool
	err    error
}

func (s *contentsServer) Select(_ context.Context, sql string, callback func(clickhouseclient.Row) error, _ ...map[string]string) error {
	if s.err != nil {
		return s.err
	}
	if s.exists && strings.Contains(sql, "system.tables") {
		row := clickhouseclient.Row{}
		row.Set("name", "t")
		return callback(row)
	}
	return nil
}

func TestChecksumDistinguishesMissingTables(t *testing.T) {
	for _, test := range []struct {
		name    string
		exists  bool
		err     error
		missing bool
	}{
		{name: "missing", missing: true}, {name: "exists without insertable columns", exists: true}, {name: "query failed", err: errors.New("permission denied")},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &impl{clickhouseClient: &contentsServer{exists: test.exists, err: test.err}}
			_, err := client.DataChecksum(context.Background(), "example", "t", "JSONEachRow", "invalid")
			if err == nil || errors.Is(err, ErrTableNotFound) != test.missing {
				t.Fatalf("missing=%v err=%v", test.missing, err)
			}
			_, exists, err := client.TableContentsChecksum(context.Background(), "example", "t")
			if test.missing {
				if err != nil || exists {
					t.Fatalf("missing table: exists=%v err=%v", exists, err)
				}
			} else if err == nil {
				t.Fatal("non-missing error was hidden")
			}
		})
	}
}
