package command

import (
	"fmt"

	client "github.com/ipni/go-libipni/find/client/http"
	"github.com/ipni/go-libipni/find/model"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/urfave/cli/v2"
)

var ProvidersCmd = &cli.Command{
	Name:  "providers",
	Usage: "Commands to get provider information",
	Subcommands: []*cli.Command{
		getProvidersCmd,
		listProvidersCmd,
	},
}

var getProvidersCmd = &cli.Command{
	Name:  "get",
	Usage: "Show information about a specific provider",
	Flags: []cli.Flag{
		indexerHostFlag,
		providerFlag,
	},
	Action: getProvidersAction,
}

var listProvidersCmd = &cli.Command{
	Name:  "list",
	Usage: "Show information about all known providers",
	Flags: []cli.Flag{
		indexerHostFlag,
	},
	Action: listProvidersAction,
}

func getProvidersAction(cctx *cli.Context) error {
	cl, err := client.New(cliIndexer(cctx, "finder"))
	if err != nil {
		return err
	}
	peerID, err := peer.Decode(cctx.String("provider"))
	if err != nil {
		return err
	}
	prov, err := cl.GetProvider(cctx.Context, peerID)
	if err != nil {
		return err
	}
	if prov == nil {
		fmt.Println("Provider not found on indexer")
		return nil
	}

	showProviderInfo(prov)
	return nil
}

func listProvidersAction(cctx *cli.Context) error {
	cl, err := client.New(cliIndexer(cctx, "finder"))
	if err != nil {
		return err
	}
	provs, err := cl.ListProviders(cctx.Context)
	if err != nil {
		return err
	}
	if len(provs) == 0 {
		fmt.Println("No providers registered with indexer")
		return nil
	}

	for _, pinfo := range provs {
		showProviderInfo(pinfo)
	}

	return nil
}

func showProviderInfo(pinfo *model.ProviderInfo) {
	fmt.Println("Provider", pinfo.AddrInfo.ID)
	fmt.Println("    Addresses:", pinfo.AddrInfo.Addrs)
	var adCidStr string
	var timeStr string
	if pinfo.LastAdvertisement.Defined() {
		adCidStr = pinfo.LastAdvertisement.String()
		timeStr = pinfo.LastAdvertisementTime
	}
	fmt.Println("    LastAdvertisement:", adCidStr)
	fmt.Println("    LastAdvertisementTime:", timeStr)
	if adCidStr != "" {
		fmt.Println("    Lag:", pinfo.Lag)
	}
	if pinfo.Publisher != nil {
		fmt.Println("    Publisher:", pinfo.Publisher.ID)
		fmt.Println("        Publisher Addrs:", pinfo.Publisher.Addrs)
		if pinfo.FrozenAt.Defined() {
			fmt.Println("    FrozenAt:", pinfo.FrozenAt.String())
		}
	} else {
		fmt.Println("    Publisher: none")
	}
	// Provider is still frozen even if there is no FrozenAt CID.
	if pinfo.FrozenAtTime != "" {
		fmt.Println("    FrozenAtTime:", pinfo.FrozenAtTime)
	}
	fmt.Println("    IndexCount:", pinfo.IndexCount)
	if pinfo.Inactive {
		fmt.Println("    Inactive: true")
	}
}
