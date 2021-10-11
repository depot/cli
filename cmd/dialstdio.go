package cmd

import (
	"fmt"
	"log"
	"os"

	"github.com/depot/cli/pkg/api"
	"github.com/depot/cli/pkg/ssh"
	"github.com/spf13/cobra"
	xssh "golang.org/x/crypto/ssh"
)

var dialStdioCommand = &cobra.Command{
	Use:    "dial-stdio",
	Hidden: true,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("dial stdio")

		pub, priv := ssh.NewKeyPair()
		fmt.Fprintf(os.Stderr, `Public key:
%s
Private key:
%s
`, pub, priv)

		init, err := api.InitBuild(pub)
		if err != nil {
			log.Fatal(err)
		}

		fmt.Fprintf(os.Stderr, "InitBuild response: %+v\n", init)

		var instance = fmt.Sprintf("%s:22", init.BuildIp)
		fmt.Fprintf(os.Stderr, "Dialing %s\n", instance)

		signer, err := xssh.ParsePrivateKey(priv)
		if err != nil {
			panic(err)
		}

		config := &xssh.ClientConfig{
			User: "clear",
			Auth: []xssh.AuthMethod{
				xssh.PublicKeys(signer),
			},
			HostKeyCallback: xssh.InsecureIgnoreHostKey(),
		}

		client, err := xssh.Dial("tcp", instance, config)
		if err != nil {
			panic(err)
		}

		defer client.Close()

		session, err := client.NewSession()
		if err != nil {
			log.Fatal("Failed to create session: ", err)
		}
		defer session.Close()

		session.Stdin = os.Stdin
		session.Stdout = os.Stdout
		session.Stderr = os.Stderr

		if err := session.Run("/bin/sh -c 'while [ ! -e /var/run/buildkit/buildkitd.sock ]; do echo waiting; sleep 1; done; sudo socat stdio /var/run/buildkit/buildkitd.sock'"); err != nil {
			log.Fatal("Failed to run: " + err.Error())
		}
	},
}

func init() {
	rootCmd.AddCommand(dialStdioCommand)
}
