package engine

import (
	"bytes"
	"fmt"

	"github.com/flswld/halo/protocol"
)

func (i *NetIf) RxEthernet(ethFrm []byte) {
	if i.Engine.Config.DebugLog {
		Log(fmt.Sprintf("rx eth frm, if: %v, len: %v, data: %02x\n", i.Config.Name, len(ethFrm), ethFrm))
	}
	ethPayload, ethDstMac, ethSrcMac, ethProto, err := protocol.ParseEthFrm(ethFrm)
	if err != nil {
		Log(fmt.Sprintf("parse ethernet frame error: %v\n", err))
		return
	}
	if !bytes.Equal(ethDstMac, protocol.BROADCAST_MAC_ADDR) && !bytes.Equal(ethDstMac, i.MacAddr) {
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

func (i *NetIf) TxEthernet(ethPayload []byte, ethDstMac []byte, ethProto uint16) bool {
	i.EthTxLock.Lock()
	defer i.EthTxLock.Unlock()
	i.EthTxBuffer = i.EthTxBuffer[0:0]
	ethFrm, err := protocol.BuildEthFrm(i.EthTxBuffer, ethPayload, ethDstMac, i.MacAddr, ethProto)
	if err != nil {
		Log(fmt.Sprintf("build ethernet frame error: %v\n", err))
		return false
	}
	if i.Engine.Config.DebugLog {
		Log(fmt.Sprintf("tx eth frm, if: %v, len: %v, data: %02x\n", i.Config.Name, len(ethFrm), ethFrm))
	}
	i.Config.EthTxFunc(ethFrm)
	return true
}
