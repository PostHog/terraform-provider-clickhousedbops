package main

import (
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6/tf6server"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/provider"
)

func main() {
	var debug bool

	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	var opts []tf6server.ServeOpt
	if debug {
		opts = append(opts, tf6server.WithManagedDebug())
	}
	err := tf6server.Serve("registry.terraform.io/ClickHouse/clickhousedbops", provider.Protocol6, opts...)
	if err != nil {
		log.Fatal(err.Error())
	}
}
