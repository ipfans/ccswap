package cli

import (
	"fmt"

	"github.com/ipfans/ccswap/pkg/switcher"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all managed accounts with usage info",
	RunE: func(cmd *cobra.Command, args []string) error {
		sw, err := switcher.NewSwitcher()
		if err != nil {
			return err
		}
		result, err := sw.ListAccounts()
		if err != nil {
			return err
		}
		fmt.Println(result)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(listCmd)
}
