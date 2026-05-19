package cli

import (
	"github.com/ipfans/ccswap/pkg/switcher"
	"github.com/spf13/cobra"
)

var switchToCmd = &cobra.Command{
	Use:   "switch-to",
	Short: "Switch to a specific account",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		sw, err := switcher.NewSwitcher()
		if err != nil {
			return err
		}
		return sw.SwitchTo(args[0])
	},
}

func init() {
	rootCmd.AddCommand(switchToCmd)
}
