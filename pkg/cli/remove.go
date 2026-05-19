package cli

import (
	"fmt"

	"github.com/ipfans/ccswap/pkg/switcher"
	"github.com/spf13/cobra"
)

var removeCmd = &cobra.Command{
	Use:   "remove-account",
	Short: "Remove an account from the managed pool",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		sw, err := switcher.NewSwitcher()
		if err != nil {
			return err
		}
		result, err := sw.RemoveAccount(args[0])
		if err != nil {
			return err
		}
		fmt.Println(result)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(removeCmd)
}
