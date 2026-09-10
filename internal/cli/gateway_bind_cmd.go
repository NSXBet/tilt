package cli

import (
	"github.com/spf13/cobra"

	"github.com/tilt-dev/tilt/internal/hud/server"
	"github.com/tilt-dev/tilt/pkg/model"
)

// newGatewayBindCmd implements the one-shot privileged bind helper used by
// --gateway-port: bind the socket under sudo, pass the file descriptor back
// to the unprivileged Tilt process over a Unix socket, and exit. It is
// hidden and never useful to run by hand.
func newGatewayBindCmd() *cobra.Command {
	var host string
	var port int
	var socket string

	cmd := &cobra.Command{
		Use:    "gateway-bind",
		Short:  "Internal: bind the gateway port and pass the listener fd to the parent process",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return server.RunGatewayBind(cmd.Context(), model.WebHost(host), server.GatewayPort(port), socket)
		},
	}

	cmd.Flags().StringVar(&host, "host", "localhost", "Host to bind")
	cmd.Flags().IntVar(&port, "port", 0, "Port to bind")
	cmd.Flags().StringVar(&socket, "socket", "", "Unix socket of the waiting parent process")
	_ = cmd.MarkFlagRequired("socket")
	return cmd
}
