//go:build darwin

package awg

import (
	"fmt"
	"os"
	"syscall"

	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
)

// openTUN оборачивает готовый дескриптор utun-интерфейса, который на iOS уже
// создал NEPacketTunnelProvider (Swift-часть достаёт его сканом getpeername
// по открытым fd). CreateTUNFromFile не создаёт новый сокет, а только
// оборачивает переданный - это единственный вариант без entitlement'ов на
// создание собственного utun.
//
// MTU не выставляем: имя интерфейса и маршруты уже проставила платформа через
// NEPacketTunnelNetworkSettings, тут это делать незачем (см. tun_linux.go).
//
// Дескриптор переходит во владение openTUN, включая её собственную неудачу:
// вызывающий не закрывает fd ни при каком исходе.
func openTUN(fd int) (tun.Device, error) {
	if err := syscall.SetNonblock(fd, true); err != nil {
		CloseTUNFD(fd)
		return nil, fmt.Errorf("awg: set tun nonblock: %w", err)
	}
	dev, err := tun.CreateTUNFromFile(os.NewFile(uintptr(fd), "tun"), 0)
	if err != nil {
		return nil, fmt.Errorf("awg: create tun: %w", err)
	}
	return dev, nil
}

// CloseTUNFD закрывает дескриптор, не дошедший до openTUN: владение переходит к
// Backend с момента Up, включая неудачную попытку, а до Up его закрывает
// вызывающий.
func CloseTUNFD(fd int) {
	_ = os.NewFile(uintptr(fd), "tun").Close()
}
