package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/urfave/cli"
)

var chatPeersCommand = cli.Command{
	Name:     "chatpeers",
	Category: "Chat",
	Usage:    "Show recommended peers to connect to for chatting.",
	Action:   actionDecorator(chatPeers),
}

func chatPeers(ctx *cli.Context) error {
	network := strings.ToLower(ctx.GlobalString("network"))

	bosNodes, err := getBosNodes(network)
	if err != nil {
		return err
	}

	conn := getClientConn(ctx, false)
	defer conn.Close()

	client := lnrpc.NewLightningClient(conn)

	graph, err := client.DescribeGraph(
		context.Background(),
		&lnrpc.ChannelGraphRequest{},
	)
	if err != nil {
		return err
	}

	type bestPolicy struct {
		fee     int64
		minHtlc int64
	}

	lowestFee := make(map[string]bestPolicy)
	for _, u := range graph.Edges {
		process := func(key string, p *lnrpc.RoutingPolicy) {
			if p == nil {
				return
			}
			amt := p.MinHtlc
			fee := p.FeeBaseMsat + amt*p.FeeRateMilliMsat/1000000

			lowest, ok := lowestFee[key]
			if !ok || fee > lowest.fee {
				lowest = bestPolicy{
					fee: fee,
				}
				lowestFee[key] = lowest
			}
			if p.MinHtlc > lowest.minHtlc {
				lowest.minHtlc = p.MinHtlc
				lowestFee[key] = lowest
			}
		}

		process(u.Node1Pub, u.Node1Policy)
		process(u.Node2Pub, u.Node2Policy)
	}

	type nodeFee struct {
		node  string
		alias string
		fee   bestPolicy
	}

	list := make([]nodeFee, 0)
	for n, f := range lowestFee {
		alias, ok := bosNodes[n]
		if !ok {
			continue
		}
		list = append(list, nodeFee{
			node:  n,
			fee:   f,
			alias: alias,
		})
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].fee.fee < list[j].fee.fee || (list[i].fee.fee == list[j].fee.fee && list[i].fee.minHtlc < list[j].fee.minHtlc)
	})

	for _, item := range list {
		fmt.Printf("%v (%v) %v msat (min_htlc %v msat)\n", item.node, item.alias, item.fee.fee, item.fee.minHtlc)
	}

	return nil
}

type BosScore struct {
	Alias     string
	PublicKey string `json:"public_key"`
}

type BosList struct {
	Scores []*BosScore
}

func getBosNodes(network string) (map[string]string, error) {
	var url string
	switch network {
	case "mainnet":
		url = "https://nodes.lightning.computer/availability/v1/btc.json"
	case "testnet":
		url = "https://nodes.lightning.computer/availability/v1/btctestnet.json"
	default:
		return nil, fmt.Errorf("no bos list for network %v", network)
	}

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	respByte := buf.Bytes()

	var bosList BosList
	if err := json.Unmarshal(respByte, &bosList); err != nil {
		return nil, err
	}

	bosNodes := make(map[string]string)
	for _, item := range bosList.Scores {
		bosNodes[item.PublicKey] = item.Alias
	}
	return bosNodes, nil
}
