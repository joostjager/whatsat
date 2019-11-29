## Lightning Network Daemon - special WHATSAT edition

This repo is a fork of [`lnd`](https://github.com/lightningnetwork/lnd) that demonstrates how the Lightning Network
can be used as an end-to-end encrypted, onion-routed, censorship-resistant, peer-to-peer chat messages protocol.

<img src="whatsat.gif" alt="screencast" width="880" />

Recent [changes to the protocol](https://github.com/lightningnetwork/lightning-rfc/pull/619) made it easier then before to attach arbitrary data to a payment. This demo leverages that by attaching a text message and a sender signature.

Ideally users would send each other 0 sat payments and only drop off fees along the way. But that is currently not supported in the protocol. Also, there are minimum htlc amount constraints on channels. As a workaround, in anticipation of a true micropayment network, some money is paid to the recipient of the message. In this demo, it is 1000 msat by default (can be configured through a command line flag). Both parties keeping a running balance of what they owe the other and send that back with the next message.

It is currently also possible to chat over Lightning without paying anything at all. The receiver of the chat message can fail the payment after having extracted the message. In Lightning, there is no charge for failed payments. This is generally considered an unintended use of the network and it may not be possible anymore in the future to leverage failure messages like that. See the [lightning-dev mailing list](https://lists.linuxfoundation.org/pipermail/lightning-dev/2019-November/002275.html). To use Whatsat in 'free' mode, run it with the `--free` command line flag.

## Usage

* Set up a Lightning Node as usual and open a channel to a well-connected node. Also make sure you have inbound liquidity too, otherwise it won't be possible to receive messages. And use public channels, otherwise people won't be able to find routes to deliver messages to you. No support for routing hints yet.

* Run `lncli chat <pubkey_or_alias>` to start chatting with your chosen destination.

  The blue checkmarks serve as delivery notifications. The amounts in blue on the right are the routing fees paid for the delivery. This
  does not include the amount paid to the recipient of the message, because it is assumed that that amount will be returned to us in the
  next reply.

  All chat messages end up in the same window. It is possible to switch to sending to a different destination by typing `/<pubkey_or_alias>` in the send box.

## Disclaimer

This code only serves to demonstrate the concept and doesn't pass the required quality checks. Use with testnet sats only.