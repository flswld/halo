package engine

import (
	"bytes"
	"fmt"

	"github.com/flswld/halo/protocol"
)

func (i *NetIf) RxEthernet(ethFrm []byte) {
	if i.Engine.DebugLog {
		Log(fmt.Sprintf("rx eth frm, len: %v, data: %v\n", len(ethFrm), ethFrm))
	}
	ethPayload, ethDstMac, ethSrcMac, ethProto, err := protocol.ParseEthFrm(ethFrm)
	if err != nil {
		Log(fmt.Sprintf("parse ethernet frame error: %v\n", err))
		return
	}
	if !bytes.Equal(ethDstMac, BROADCAST_MAC_ADDR) && !bytes.Equal(ethDstMac, i.MacAddr) {
		return
	}
	switch ethProto {
	case protocol.ETH_PROTO_ARP:
		i.HandleArp(ethPayload, ethSrcMac)
	case protocol.ETH_PROTO_IPV4:
		i.RxIpv4(ethPayload)
	default:
	}
}

func (i *NetIf) TxEthernet(ethPayload []byte, ethDstMac []byte, ethProto uint16) []byte {
	ethFrm, err := protocol.BuildEthFrm(ethPayload, ethDstMac, i.MacAddr, ethProto)
	if err != nil {
		Log(fmt.Sprintf("build ethernet frame error: %v\n", err))
		return nil
	}
	if i.Engine.DebugLog {
		Log(fmt.Sprintf("tx eth frm, len: %v, data: %v\n", len(ethFrm), ethFrm))
	}
	i.EthTxChan <- ethFrm
	return ethFrm
}
