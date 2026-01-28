package engine

import (
	"bytes"
	"fmt"
	"time"

	"github.com/flswld/halo/mem"
	"github.com/flswld/halo/protocol"
)

func (i *NetIf) RxEthernet(ethFrm []byte) {
	if i.Router.Config.DebugLog {
		Log(fmt.Sprintf("rx eth frm, if: %v, len: %v, data: %02x\n", i.Config.Name, len(ethFrm), ethFrm))
	}
	ethPayload, ethDstMac, ethSrcMac, ethProto, err := protocol.ParseEthFrm(ethFrm)
	if err != nil {
		Log(fmt.Sprintf("parse ethernet frame error: %v\n", err))
		return
	}
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
	i.EthTxBuffer = i.EthTxBuffer[0:0]
	ethFrm, err := protocol.BuildEthFrm(i.EthTxBuffer, ethPayload, ethDstMac, i.MacAddr, ethProto)
	if err != nil {
		Log(fmt.Sprintf("build ethernet frame error: %v\n", err))
		i.EthTxLock.Unlock()
		return false
	}
	if i.Router.Config.DebugLog {
		Log(fmt.Sprintf("tx eth frm, if: %v, len: %v, data: %02x\n", i.Config.Name, len(ethFrm), ethFrm))
	}
	i.Config.EthTxFunc(ethFrm)
	i.EthTxLock.Unlock()
	return true
}

type SwitchMacAddr struct {
	MacAddr    [6]byte            // mac地址
	NetIf      mem.StaticString64 // 网卡名
	CreateTime uint32             // 创建时间
}

func (s *SwitchPort) RxEthernet(ethFrm []byte) {
	ethPayload, ethDstMac, ethSrcMac, ethProto, err := protocol.ParseEthFrm(ethFrm)
	if err != nil {
		Log(fmt.Sprintf("parse ethernet frame error: %v\n", err))
		return
	}
	s.HandleEthernet(ethPayload, ethDstMac, ethSrcMac, ethProto)
}

func (s *SwitchPort) HandleEthernet(ethPayload []byte, ethDstMac []byte, ethSrcMac []byte, ethProto uint16) {
	if ethSrcMac[0]&0x01 == 0x01 {
		return
	}
	s.Switch.SwitchMacAddrLock.RLock()
	srcMacAddr, exist := s.Switch.SwitchMacAddrTable.Get(MacAddrHash(ethSrcMac))
	s.Switch.SwitchMacAddrLock.RUnlock()
	if !exist {
		s.Switch.SwitchMacAddrLock.Lock()
		srcMacAddr = mem.MallocType[SwitchMacAddr](s.Switch.StaticAllocator, 1)
		if srcMacAddr == nil {
			s.Switch.SwitchMacAddrLock.Unlock()
			return
		}
		ok := s.Switch.SwitchMacAddrTable.Set(MacAddrHash(ethSrcMac), srcMacAddr)
		if !ok {
			mem.FreeType[SwitchMacAddr](s.Switch.StaticAllocator, srcMacAddr)
			s.Switch.SwitchMacAddrLock.Unlock()
			return
		}
		s.Switch.SwitchMacAddrLock.Unlock()
	}
	copy(srcMacAddr.MacAddr[:], ethSrcMac)
	srcMacAddr.NetIf.Set(s.Config.Name)
	srcMacAddr.CreateTime = s.Switch.TimeNow
	s.Switch.SwitchMacAddrLock.RLock()
	dstMacAddr, exist := s.Switch.SwitchMacAddrTable.Get(MacAddrHash(ethDstMac))
	s.Switch.SwitchMacAddrLock.RUnlock()
	if exist {
		switchPort := s.Switch.GetSwitchPort(dstMacAddr.NetIf.Get())
		switchPort.TxEthernet(ethPayload, ethDstMac, ethSrcMac, ethProto)
	} else {
		for _, switchPort := range s.Switch.SwitchPortMap {
			if switchPort.Config.Name == s.Config.Name {
				continue
			}
			if switchPort.Config.VlanId != s.Config.VlanId {
				continue
			}
			switchPort.TxEthernet(ethPayload, ethDstMac, ethSrcMac, ethProto)
		}
	}
}

func (s *SwitchPort) TxEthernet(ethPayload []byte, ethDstMac []byte, ethSrcMac []byte, ethProto uint16) bool {
	s.EthTxLock.Lock()
	s.EthTxBuffer = s.EthTxBuffer[0:0]
	ethFrm, err := protocol.BuildEthFrm(s.EthTxBuffer, ethPayload, ethDstMac, ethSrcMac, ethProto)
	if err != nil {
		Log(fmt.Sprintf("build ethernet frame error: %v\n", err))
		s.EthTxLock.Unlock()
		return false
	}
	s.Config.EthTxFunc(ethFrm)
	s.EthTxLock.Unlock()
	return true
}

func (s *Switch) SwitchMacAddrClear() {
	ticker := time.NewTicker(time.Second * 1)
	for {
		<-ticker.C
		if s.Stop.Load() {
			break
		}
		s.SwitchMacAddrLock.Lock()
		s.SwitchMacAddrTable.For(func(macAddrHash MacAddrHash, switchMacAddr *SwitchMacAddr) (next bool) {
			if s.TimeNow > switchMacAddr.CreateTime+300 {
				s.SwitchMacAddrTable.Del(macAddrHash)
				mem.FreeType[SwitchMacAddr](s.StaticAllocator, switchMacAddr)
			}
			return true
		})
		s.SwitchMacAddrLock.Unlock()
	}
	s.StopWaitGroup.Done()
}

func (s *Switch) ListSwitchMacAddr() []*SwitchMacAddr {
	s.SwitchMacAddrLock.Lock()
	defer s.SwitchMacAddrLock.Unlock()
	ret := make([]*SwitchMacAddr, 0)
	s.SwitchMacAddrTable.For(func(key MacAddrHash, value *SwitchMacAddr) (next bool) {
		v := *value
		ret = append(ret, &v)
		return true
	})
	return ret
}
