package app

import (
	"fmt"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

func (o *SharedOptions) PrintRaw(cmd *cobra.Command) {
	data, err := yaml.Marshal(o.RawSharedOptions)
	if err != nil {
		cmd.Println(fmt.Errorf("error marshalling shared options: %w", err).Error())
		return
	}
	cmd.Print(string(data))
}

func (o *SharedOptions) PrintCompleted(cmd *cobra.Command) {
	raw := map[string]any{
		"platformCluster": o.PlatformCluster.APIServerEndpoint(),
		"providerName":    o.ProviderName,
	}
	data, err := yaml.Marshal(raw)
	if err != nil {
		cmd.Println(fmt.Errorf("error marshalling completed options: %w", err).Error())
		return
	}
	cmd.Print(string(data))
}
