package kcp

import (
	"strconv"
	"strings"
	"sync"
)

type UDPAddr struct {
	IpAddr []byte
	Port   uint16
}

func ResolveUDPAddr(address string) (*UDPAddr, error) {
	udpAddrSplit := strings.Split(address, ":")
	ipAddrSplit := strings.Split(udpAddrSplit[0], ".")
	r := new(UDPAddr)
	r.IpAddr = make([]byte, 4)
	for i := 0; i < 4; i++ {
		ipAddr, err := strconv.Atoi(ipAddrSplit[i])
		if err != nil {
			return nil, err
		}
		r.IpAddr[i] = uint8(ipAddr)
	}
	port, err := strconv.Atoi(udpAddrSplit[1])
	if err != nil {
		return nil, err
	}
	r.Port = uint16(port)
	return r, nil
}

func (u *UDPAddr) String() string {
	r := ""
	for i := 0; i < 4; i++ {
		r += strconv.Itoa(int(u.IpAddr[i]))
		if i < 3 {
			r += "."
		}
	}
	r += ":"
	r += strconv.Itoa(int(u.Port))
	return r
}

func ConvUdpAddrToUint64(udpAddr *UDPAddr) (udpAddrUint64 uint64) {
	udpAddrUint64 = uint64(0)
	udpAddrUint64 += uint64(udpAddr.IpAddr[0]) << 40
	udpAddrUint64 += uint64(udpAddr.IpAddr[1]) << 32
	udpAddrUint64 += uint64(udpAddr.IpAddr[2]) << 24
	udpAddrUint64 += uint64(udpAddr.IpAddr[3]) << 16
	udpAddrUint64 += uint64(uint8(udpAddr.Port>>8)) << 8
	udpAddrUint64 += uint64(uint8(udpAddr.Port)) << 0
	return udpAddrUint64
}

var udpConnMap = make(map[uint64]*UDPConn)
var udpConnMapLock sync.RWMutex

func UdpRx(udpPayload []byte, udpSrcPort uint16, udpDstPort uint16, ipv4SrcAddr []byte) {
	udpAddrUint64 := ConvUdpAddrToUint64(&UDPAddr{
		IpAddr: []byte{0x00, 0x00, 0x00, 0x00},
		Port:   udpDstPort,
	})
	udpConnMapLock.RLock()
	udpConn, exist := udpConnMap[udpAddrUint64]
	udpConnMapLock.RUnlock()
	if !exist {
		return
	}
	udpConn.LocalRecvQueue <- &UdpPacket{
		Data: udpPayload,
		Addr: &UDPAddr{
			IpAddr: ipv4SrcAddr,
			Port:   udpSrcPort,
		},
	}
}

var UdpTx func(udpPayload []byte, udpSrcPort uint16, udpDstPort uint16, ipv4DstAddr []byte) []byte = nil

type UdpPacket struct {
	Data []byte
	Addr *UDPAddr
}

type UDPConn struct {
	LocalIpAddr    []byte
	LocalPort      uint16
	LocalRecvQueue chan *UdpPacket
}

func ListenUDP(laddr *UDPAddr) (*UDPConn, error) {
	r := new(UDPConn)
	r.LocalIpAddr = laddr.IpAddr
	r.LocalPort = laddr.Port
	r.LocalRecvQueue = make(chan *UdpPacket, 1000)
	udpAddrUint64 := ConvUdpAddrToUint64(laddr)
	udpConnMapLock.Lock()
	udpConnMap[udpAddrUint64] = r
	udpConnMapLock.Unlock()
	return r, nil
}

func (u *UDPConn) ReadFrom(p []byte) (n int, addr *UDPAddr, err error) {
	udpPacket := <-u.LocalRecvQueue
	copy(p, udpPacket.Data)
	n = len(udpPacket.Data)
	addr = udpPacket.Addr
	return n, addr, nil
}

func (u *UDPConn) WriteTo(p []byte, addr *UDPAddr) (n int, err error) {
	if UdpTx == nil {
		return
	}
	UdpTx(p, u.LocalPort, addr.Port, addr.IpAddr)
	return len(p), err
}

func (u *UDPConn) SendEnetNotifyToPeer(enet *Enet) {
	data := BuildEnet(enet.ConnType, enet.EnetType, enet.SessionId, enet.Conv)
	if data == nil {
		return
	}
	udpAddr, err := ResolveUDPAddr(enet.Addr)
	if err != nil {
		return
	}
	if UdpTx == nil {
		return
	}
	UdpTx(data, u.LocalPort, udpAddr.Port, udpAddr.IpAddr)
}

func (u *UDPConn) LocalAddr() *UDPAddr {
	r := new(UDPAddr)
	r.IpAddr = u.LocalIpAddr
	r.Port = u.LocalPort
	return r
}

func (u *UDPConn) Close() error {
	return nil
}

func (u *UDPConn) SetReadBuffer(bytes int) error {
	return nil
}

func (u *UDPConn) SetWriteBuffer(bytes int) error {
	return nil
}
