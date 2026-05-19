package cli

import (
	"github.com/ipfans/ccswap/pkg/switcher"
	"github.com/spf13/cobra"
)

var switchCmd = &cobra.Command{
	Use:   "switch",
	Short: "Switch to the next account in sequence",
	RunE: func(cmd *cobra.Command, args []string) error {
		sw, err := switcher.NewSwitcher()
		if err != nil {
			return err
		}
		return sw.Switch()
	},
}

func init() {
	rootCmd.AddCommand(switchCmd)
}
