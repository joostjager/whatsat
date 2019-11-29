// +build routerrpc

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/routing/route"
	"google.golang.org/grpc"

	"github.com/jroimartin/gocui"
	"github.com/lightningnetwork/lnd/lnrpc/routerrpc"
	"github.com/urfave/cli"
)

var chatCommand = cli.Command{
	Name:      "chat",
	Category:  "Chat",
	ArgsUsage: "recipient_pubkey",
	Usage:     "Use lnd as a p2p messenger application.",
	Action:    actionDecorator(chat),
	Flags: []cli.Flag{
		cli.BoolFlag{
			Name:  "free",
			Usage: "chat for free by sending payment to unknown hashes",
		},
		cli.Uint64Flag{
			Name:  "amt_msat",
			Usage: "payment amount per chat message",
			Value: 1000,
		},
		cli.StringFlag{
			Name:  "log",
			Usage: "file to log chat traffic to",
		},
	},
}

type messageState uint8

const (
	statePending messageState = iota

	stateDelivered

	stateFailed
)

type chatLine struct {
	text   string
	sender route.Vertex
	state  messageState
	fee    uint64
}

var (
	msgLines       []chatLine
	destination    *route.Vertex
	runningBalance map[route.Vertex]int64 = make(map[route.Vertex]int64)

	keyToAlias = make(map[route.Vertex]string)
	aliasToKey = make(map[string]route.Vertex)

	self route.Vertex

	logFile string
)

func initAliasMaps(conn *grpc.ClientConn) error {
	client := lnrpc.NewLightningClient(conn)

	graph, err := client.DescribeGraph(
		context.Background(),
		&lnrpc.ChannelGraphRequest{},
	)
	if err != nil {
		return err
	}

	aliasCount := make(map[string]int)
	for _, node := range graph.Nodes {
		alias := node.Alias
		aliasCount[alias]++
	}

	for _, node := range graph.Nodes {
		alias := node.Alias

		key, err := route.NewVertexFromStr(node.PubKey)
		if err != nil {
			return err
		}

		if aliasCount[alias] > 1 {
			alias += "-" + node.PubKey[:6]
		}

		aliasToKey[alias] = key
		aliasToKey[key.String()] = key

		keyToAlias[key] = alias
	}

	info, err := client.GetInfo(context.Background(), &lnrpc.GetInfoRequest{})
	if err != nil {
		return err
	}

	self, err = route.NewVertexFromStr(info.IdentityPubkey)
	if err != nil {
		return err
	}

	return nil
}

func setDest(destStr string) {
	dest, err := route.NewVertexFromStr(destStr)
	if err == nil {
		destination = &dest
	}

	if dest, ok := aliasToKey[destStr]; ok {
		destination = &dest
	}
}

func chat(ctx *cli.Context) error {
	fmt.Println("\x07")

	free := ctx.Bool("free")
	chatMsgAmt := int64(ctx.Uint64("amt_msat"))
	logFile = ctx.String("log")

	conn := getClientConn(ctx, false)
	defer conn.Close()

	err := initAliasMaps(conn)
	if err != nil {
		return err
	}

	if ctx.NArg() != 0 {
		destStr := ctx.Args().First()
		setDest(destStr)
	}

	client := routerrpc.NewRouterClient(conn)

	req := &routerrpc.ReceiveChatMessagesRequest{}
	rpcCtx := context.Background()
	stream, err := client.ReceiveChatMessages(rpcCtx, req)
	if err != nil {
		return err
	}

	g, err := gocui.NewGui(gocui.OutputNormal)
	if err != nil {
		log.Panicln(err)
	}
	defer g.Close()

	g.SetManagerFunc(layout)

	if err := g.SetKeybinding("", gocui.KeyCtrlC, gocui.ModNone, quit); err != nil {
		log.Panicln(err)
	}

	addMsg := func(line chatLine) int {
		msgLines = append(msgLines, line)

		updateView(g)

		return len(msgLines) - 1
	}

	sendMessage := func(g *gocui.Gui, v *gocui.View) error {
		if len(v.BufferLines()) == 0 {
			return nil
		}
		newMsg := v.BufferLines()[0]

		g.Update(func(g *gocui.Gui) error {
			v.Clear()
			if err := v.SetCursor(0, 0); err != nil {
				return err
			}
			if err := v.SetOrigin(0, 0); err != nil {
				return err
			}
			return nil
		})

		if newMsg[0] == '/' {
			destHex := newMsg[1:]
			setDest(destHex)

			updateView(g)

			return nil
		}

		if destination == nil {
			return nil
		}

		logMsg(self, newMsg)

		msgIdx := addMsg(chatLine{
			sender: self,
			text:   newMsg,
		})

		payAmt := runningBalance[*destination]
		if payAmt < chatMsgAmt {
			payAmt = chatMsgAmt
		}

		req := routerrpc.SendPaymentRequest{
			ChatMessage:    newMsg,
			ChatFree:       free,
			AmtMsat:        payAmt,
			FinalCltvDelta: 40,
			Dest:           destination[:],
			FeeLimitMsat:   chatMsgAmt * 10,
			TimeoutSeconds: 30,
		}

		go func() {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream, err := client.SendPayment(ctx, &req)
			if err != nil {
				g.Update(func(g *gocui.Gui) error {
					return err
				})
				return
			}

			for {
				status, err := stream.Recv()
				if err != nil {
					break
				}

				switch status.State {
				case routerrpc.PaymentState_SUCCEEDED:
					msgLines[msgIdx].fee = uint64(status.Route.TotalFeesMsat)
					runningBalance[*destination] -= payAmt
					fallthrough

				case routerrpc.PaymentState_FAILED_INCORRECT_PAYMENT_DETAILS:
					msgLines[msgIdx].state = stateDelivered
					updateView(g)
					break

				case routerrpc.PaymentState_IN_FLIGHT:

				default:
					msgLines[msgIdx].state = stateFailed
					updateView(g)
					break
				}
			}
		}()

		return nil
	}

	err = g.SetKeybinding("send", gocui.KeyEnter, gocui.ModNone, sendMessage)
	if err != nil {
		return err
	}

	go func() {
		for {
			chatMsg, err := stream.Recv()
			if err != nil {
				g.Update(func(g *gocui.Gui) error {
					return err
				})
				return
			}

			sender, _ := route.NewVertexFromBytes(chatMsg.SenderPubkey)
			if destination == nil {
				destination = &sender
			}

			logMsg(sender, chatMsg.Text)

			addMsg(chatLine{
				sender: sender,
				text:   chatMsg.Text,
			})

			runningBalance[*destination] += chatMsg.AmtReceivedMsat
		}
	}()

	if err := g.MainLoop(); err != nil && err != gocui.ErrQuit {
		return err
	}

	return nil
}

func layout(g *gocui.Gui) error {
	g.Cursor = true

	maxX, maxY := g.Size()
	if v, err := g.SetView("messages", 0, 0, maxX-1, maxY-5); err != nil {
		if err != gocui.ErrUnknownView {
			return err
		}
		v.Title = " Messages "
	}

	if v, err := g.SetView("send", 0, maxY-4, maxX-1, maxY-1); err != nil {
		if _, err := g.SetCurrentView("send"); err != nil {
			return err
		}

		if err != gocui.ErrUnknownView {
			return err
		}

		v.Editable = true
	}

	updateView(g)

	return nil
}

func quit(g *gocui.Gui, v *gocui.View) error {
	return gocui.ErrQuit
}

func updateView(g *gocui.Gui) {

	sendView, _ := g.View("send")
	if destination == nil {
		sendView.Title = " Set a destination by typing /pubkey "
	} else {
		alias := keyToAlias[*destination]
		sendView.Title = fmt.Sprintf(" Send to %v [balance: %v msat]",
			alias, runningBalance[*destination])
	}

	messagesView, _ := g.View("messages")
	g.Update(func(g *gocui.Gui) error {
		messagesView.Clear()
		cols, rows := messagesView.Size()

		startLine := len(msgLines) - rows
		if startLine < 0 {
			startLine = 0
		}

		for _, line := range msgLines[startLine:] {
			text := line.text

			var amtDisplay string
			if line.state == stateDelivered {
				amtDisplay = formatMsat(line.fee)
			}

			maxTextFieldLen := cols - len(amtDisplay) - 20
			maxTextLen := maxTextFieldLen
			if line.state != statePending {
				maxTextLen -= 2
			}
			if len(text) > maxTextLen {
				text = text[:maxTextLen-3] + "..."
			}
			paddingLen := maxTextFieldLen - len(text)
			switch line.state {
			case stateDelivered:
				text += " \x1b[34m✔️\x1b[0m"
				paddingLen -= 2
			case stateFailed:
				text += " \x1b[31m✘\x1b[0m"
				paddingLen -= 2
			}

			text += strings.Repeat(" ", paddingLen)

			fmt.Fprintf(messagesView, "%16v: %v \x1b[34m%v\x1b[0m",
				keyToAlias[line.sender],
				text, amtDisplay,
			)

			fmt.Fprintln(messagesView)
		}

		return nil
	})
}

func logMsg(sender route.Vertex, msg string) {
	if logFile == "" {
		return
	}

	text := fmt.Sprintf("%v %-16v %v\n",
		time.Now().Format("2006-01-02 15:04:05.000000"), keyToAlias[sender], msg)

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}

	defer f.Close()

	if _, err = f.WriteString(text); err != nil {
		panic(err)
	}
}

func formatMsat(msat uint64) string {
	wholeSats := msat / 1000
	msats := msat % 1000
	var msatsStr string
	if msats > 0 {
		msatsStr = fmt.Sprintf(".%03d", msats)
		msatsStr = strings.TrimRight(msatsStr, "0")
	}
	return fmt.Sprintf("[%d%-4s sat]",
		wholeSats, msatsStr,
	)
}
