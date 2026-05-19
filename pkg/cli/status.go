package cli

import (
	"fmt"

	"github.com/ipfans/ccswap/pkg/switcher"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show active account identity and usage",
	RunE: func(cmd *cobra.Command, args []string) error {
		sw, err := switcher.NewSwitcher()
		if err != nil {
			return err
		}
		result, err := sw.Status()
		if err != nil {
			return err
		}
		fmt.Println(result)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
