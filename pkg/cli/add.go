package cli

import (
	"fmt"

	"github.com/ipfans/ccswap/pkg/switcher"
	"github.com/spf13/cobra"
)

var addCmd = &cobra.Command{
	Use:   "add-account",
	Short: "Add current Claude Code account to the managed pool",
	RunE: func(cmd *cobra.Command, args []string) error {
		sw, err := switcher.NewSwitcher()
		if err != nil {
			return err
		}
		result, err := sw.AddAccount()
		if err != nil {
			return err
		}
		fmt.Println(result)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(addCmd)
}
