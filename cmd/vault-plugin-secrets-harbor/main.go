// Command vault-plugin-secrets-harbor is a HashiCorp Vault secrets engine
// plugin that issues short-lived Harbor robot accounts.
package main

import (
	"fmt"
	"os"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/vault/api"
	"github.com/hashicorp/vault/sdk/plugin"

	"github.com/corelyr-oss/vault-plugin-secrets-harbor/internal/backend"
)

// version is injected at build time via -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	apiClientMeta := &api.PluginAPIClientMeta{}
	flags := apiClientMeta.FlagSet()
	showVersion := flags.Bool("version", false, "print the plugin version and exit")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if *showVersion {
		fmt.Println(version)
		return
	}

	backend.Version = version

	tlsProviderFunc := api.VaultPluginTLSProvider(apiClientMeta.GetTLSConfig())
	if err := plugin.ServeMultiplex(&plugin.ServeOpts{
		BackendFactoryFunc: backend.Factory,
		TLSProviderFunc:    tlsProviderFunc,
	}); err != nil {
		hclog.New(&hclog.LoggerOptions{}).Error("plugin shutting down", "error", err)
		os.Exit(1)
	}
}
