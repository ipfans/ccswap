package cli

import (
	"github.com/ipfans/ccswap/pkg/switcher"
	"github.com/spf13/cobra"
)

var autoCmd = &cobra.Command{
	Use:   "auto",
	Short: "Auto-switch to a healthy account based on usage threshold",
	RunE: func(cmd *cobra.Command, args []string) error {
		threshold, err := cmd.Flags().GetFloat64("threshold")
		if err != nil {
			return err
		}
		sw, err := switcher.NewSwitcher()
		if err != nil {
			return err
		}
		return sw.AutoSwitch(threshold)
	},
}

func init() {
	autoCmd.Flags().Float64("threshold", 80, "Usage percentage threshold (0-100) above which an account is considered unhealthy")
	rootCmd.AddCommand(autoCmd)
}
