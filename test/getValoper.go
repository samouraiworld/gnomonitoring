package main

import (
	"errors"
	"fmt"
	"log"
	"regexp"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
)

type Valoper struct {
	Name    string
	Address string
}

type Client struct {
	gc *gnoclient.Client
}

func (c Client) GetValopers() ([]Valoper, error) {
	resp, err := c.gc.RPCClient.ABCIQuery("vm/qeval", []byte(`gno.land/r/gnoland/valopers.Render("")`))
	if err != nil {
		return []Valoper{}, err
	} else if err := resp.Response.Error; err != nil && err.Error() != "" {
		return []Valoper{}, errors.New(err.Error())
	}

	re := regexp.MustCompile(`\[\s*([^\]]+?)\s*]\(/r/gnoland/valopers:([a-z0-9]+)\)`)
	matches := re.FindAllStringSubmatch(string(resp.Response.Data), -1)

	valopers := make([]Valoper, len(matches))
	for i, m := range matches {
		valopers[i] = Valoper{
			Name:    m[1],
			Address: m[2],
		}
	}

	return valopers, nil
}

func main() {
	rpc, err := rpcclient.NewHTTPClient("https://rpc.test6.testnets.gno.land")
	if err != nil {
		log.Fatal(err)
	}

	gc := &gnoclient.Client{RPCClient: rpc}
	client := Client{gc: gc}

	valopers, err := client.GetValopers()
	if err != nil {
		log.Fatal(err)
	}
	for _, val := range valopers {
		fmt.Printf("%s → %s\n", val.Name, val.Address)
	}
}
