package main

import (
	"fmt"
	"os"

	"github.com/openmcp-project/cluster-provider-capi/cmd/cluster-provider-capi/app"
)

func main() {
	cmd := app.NewClusterProviderCommand()
	if err := cmd.Execute(); err != nil {
		fmt.Print(err)
		os.Exit(1)
	}
}
