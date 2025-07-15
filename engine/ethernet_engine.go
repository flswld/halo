package engine

import (
	"bytes"
	"fmt"
	"time"

	"github.com/flswld/halo/mem"
	"github.com/flswld/halo/protocol"
)

type SwitchMacAddr struct {
	MacAddr    [6]byte            // mac地址
	NetIf      mem.StaticString64 // 网卡名
	CreateTime uint32             // 创建时间
}

func (i *NetIf) RxEthernet(ethFrm []byte) {
	if i.Engine.Config.DebugLog {
		Log(fmt.Sprintf("rx eth frm, if: %v, len: %v, data: %02x\n", i.Config.Name, len(ethFrm), ethFrm))
	}
	ethPayload, ethDstMac, ethSrcMac, ethProto, err := protocol.ParseEthFrm(ethFrm)
	if err != nil {
		Log(fmt.Sprintf("parse ethernet frame error: %v\n", err))
		return
	}
	if i.Config.SwitchMode {
		forward := true
		for _, netIf := range i.Engine.NetIfMap {
			if !netIf.Config.SwitchMode {
				continue
			}
			if netIf.Config.SwitchGroup != i.Config.SwitchGroup {
				continue
			}
			if netIf.MacAddr == nil {
				continue
			}
			netIf.HandleEthernet(ethPayload, ethSrcMac, ethDstMac, ethProto)
			if bytes.Equal(ethDstMac, netIf.MacAddr) {
				forward = false
			}
		}
		if ethDstMac[0]&0x01 == 0x01 {
			forward = true
		}
		if !forward {
			return
		}
		i.Engine.SwitchMacAddrLock.RLock()
		srcMacAddr, exist := i.Engine.SwitchMacAddrTable.Get(MacAddrHash(ethSrcMac))
		i.Engine.SwitchMacAddrLock.RUnlock()
		if !exist && ethSrcMac[0]&0x01 != 0x01 {
			i.Engine.SwitchMacAddrLock.Lock()
			srcMacAddr = mem.MallocType[SwitchMacAddr](i.Engine.StaticHeap, 1)
			if srcMacAddr == nil {
				i.Engine.SwitchMacAddrLock.Unlock()
				return
			}
			ok := i.Engine.SwitchMacAddrTable.Set(MacAddrHash(ethSrcMac), srcMacAddr)
			if !ok {
				mem.FreeType[SwitchMacAddr](i.Engine.StaticHeap, srcMacAddr)
				i.Engine.SwitchMacAddrLock.Unlock()
				return
			}
			i.Engine.SwitchMacAddrLock.Unlock()
		}
		copy(srcMacAddr.MacAddr[:], ethSrcMac)
		srcMacAddr.NetIf.Set(i.Config.Name)
		srcMacAddr.CreateTime = i.Engine.TimeNow
		i.Engine.SwitchMacAddrLock.RLock()
		dstMacAddr, exist := i.Engine.SwitchMacAddrTable.Get(MacAddrHash(ethDstMac))
		i.Engine.SwitchMacAddrLock.RUnlock()
		if exist {
			netIf := i.Engine.GetNetIf(dstMacAddr.NetIf.Get())
			netIf.TxEthernet(ethPayload, ethDstMac, ethProto)
		} else {
			for _, netIf := range i.Engine.NetIfMap {
				if netIf.Config.Name == i.Config.Name {
					continue
				}
				if !netIf.Config.SwitchMode {
					continue
				}
				if netIf.Config.SwitchGroup != i.Config.SwitchGroup {
					continue
				}
				netIf.TxEthernet(ethPayload, ethDstMac, ethProto)
			}
		}
		return
	}
	i.HandleEthernet(ethPayload, ethSrcMac, ethDstMac, ethProto)
}

func (i *NetIf) HandleEthernet(ethPayload []byte, ethSrcMac []byte, ethDstMac []byte, ethProto uint16) {
	i.EthRxLock.Lock()
	defer i.EthRxLock.Unlock()
	if bytes.Equal(ethDstMac, i.MacAddr) || bytes.Equal(ethDstMac, protocol.BROADCAST_MAC_ADDR) {
		switch ethProto {
		case protocol.ETH_PROTO_ARP:
			i.HandleArp(ethPayload, ethSrcMac)
		case protocol.ETH_PROTO_IPV4:
			i.RxIpv4(ethPayload)
		default:
		}
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

func (e *Engine) SwitchMacAddrClear() {
	ticker := time.NewTicker(time.Second * 1)
	for {
		<-ticker.C
		if e.Stop.Load() {
			break
		}
		e.SwitchMacAddrLock.Lock()
		e.SwitchMacAddrTable.For(func(macAddrHash MacAddrHash, switchMacAddr *SwitchMacAddr) (next bool) {
			if e.TimeNow > switchMacAddr.CreateTime+300 {
				e.SwitchMacAddrTable.Del(macAddrHash)
				mem.FreeType[SwitchMacAddr](e.StaticHeap, switchMacAddr)
			}
			return true
		})
		e.SwitchMacAddrLock.Unlock()
	}
	e.StopWaitGroup.Done()
}
