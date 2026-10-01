package dictionary

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

func TestDictionaryCreateStatementIsSensitive(t *testing.T) {
	response := resource.SchemaResponse{}
	(&Resource{}).Schema(context.Background(), resource.SchemaRequest{}, &response)
	if !response.Schema.Attributes["create_statement"].IsSensitive() {
		t.Fatal("SHOW CREATE may contain the dictionary source password")
	}
}
